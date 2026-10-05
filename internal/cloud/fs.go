// Copyright (C) 2026 Massimo Cavalleri <massimo.cavalleri@gmail.com>
//
// This file is part of shfm.
//
// shfm is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// shfm is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with shfm.  If not, see <https://www.gnu.org/licenses/>.

//go:build cloud

package cloud

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"sync"
	"syscall"
	"time"

	"shfm/internal/vfs"
)

// backend is what a cloud service implements; FS turns it into a
// vfs.FileSystem. Paths are clean, absolute, '/'-separated paths within
// the account's storage ("/" being its root). Errors wrap os.ErrNotExist,
// os.ErrExist, os.ErrPermission or ErrAuthorization where they mean that.
type backend interface {
	stat(ctx context.Context, p string) (vfs.Entry, error)
	list(ctx context.Context, p string) ([]vfs.Entry, error)
	mkdir(ctx context.Context, p string) error
	// createEmpty creates an empty file; os.ErrExist if p exists.
	createEmpty(ctx context.Context, p string) error
	// remove deletes p, recursively for a folder: into the service's
	// trash, where it has one.
	remove(ctx context.Context, p string) error
	// move renames/moves from to to, replacing a file at to.
	move(ctx context.Context, from, to string) error
	// download reads p from byte off to its end.
	download(ctx context.Context, p string, off int64) (io.ReadCloser, error)
	// upload creates or replaces file p with r's content, size bytes
	// long; size is -1 (unknown) only for a backend whose caps say
	// streamUpload.
	upload(ctx context.Context, p string, size int64, r io.Reader) error
	space(ctx context.Context) (total, free uint64, err error)
	// setModTime sets p's modification time (caps.modTime only).
	setModTime(ctx context.Context, p string, t time.Time) error
	// account returns the account's e-mail address (or user name).
	account(ctx context.Context) (string, error)
}

// caps are what a backend can and can't do, which decides the optional
// vfs interfaces its FS implements.
type caps struct {
	// streamUpload: upload accepts content of unknown size. Without it,
	// FS implements vfs.SizedCreator, and Create spools the content to a
	// temporary file to learn its size.
	streamUpload bool
	// modTime: setModTime works (vfs.TimesSetter).
	modTime bool
	// home is the folder the account opens at ("/" when empty): the
	// account's own files, below the root listing the service's places.
	home string
}

// FS is a cloud storage account as a vfs.FileSystem. Safe for concurrent
// use; every operation is a request (or a few) to the service.
type FS struct {
	acc   Account
	b     backend
	caps  caps
	label string

	ctx    context.Context // cancelled by Close, aborting what's in flight
	cancel context.CancelFunc
}

// newFS wraps b, returning the FS variant implementing exactly the
// optional interfaces b supports.
func newFS(acc Account, b backend, c caps) vfs.FileSystem {
	ctx, cancel := context.WithCancel(context.Background())
	f := &FS{acc: acc, b: b, caps: c, label: Label(acc.Provider, acc.User), ctx: ctx, cancel: cancel}
	switch {
	case c.modTime && !c.streamUpload:
		return &timesSizedFS{f}
	case c.modTime:
		return &timesFS{f}
	case !c.streamUpload:
		return &sizedFS{f}
	}
	return f
}

// timesFS adds vfs.TimesSetter; sizedFS adds vfs.SizedCreator.
type (
	timesFS      struct{ *FS }
	sizedFS      struct{ *FS }
	timesSizedFS struct{ *FS }
)

func (f *timesFS) Chtimes(p string, atime, mtime time.Time) error      { return f.chtimes(p, mtime) }
func (f *timesSizedFS) Chtimes(p string, atime, mtime time.Time) error { return f.chtimes(p, mtime) }
func (f *sizedFS) CreateSized(p string, size int64) (io.WriteCloser, error) {
	return f.createSized(p, size)
}
func (f *timesSizedFS) CreateSized(p string, size int64) (io.WriteCloser, error) {
	return f.createSized(p, size)
}

var (
	_ vfs.FileSystem         = (*FS)(nil)
	_ vfs.RandomAccessOpener = (*FS)(nil)
	_ vfs.Redialer           = (*FS)(nil)
	_ vfs.SpaceReporter      = (*FS)(nil)
	_ vfs.ServiceTrash       = (*FS)(nil)
	_ vfs.TimesSetter        = (*timesFS)(nil)
	_ vfs.SizedCreator       = (*sizedFS)(nil)
	_ vfs.TimesSetter        = (*timesSizedFS)(nil)
	_ vfs.SizedCreator       = (*timesSizedFS)(nil)
	_ vfs.SentReporter       = (*pipeWriter)(nil)
	_ vfs.SentReporter       = (*spoolWriter)(nil)
)

// clean normalizes a path from the UI or the FUSE mount.
func clean(p string) string { return path.Clean("/" + p) }

// pathErr wraps err with the operation and path, as os functions do.
func pathErr(op, p string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return &fs.PathError{Op: op, Path: p, Err: err}
}

func (f *FS) Kind() vfs.Kind { return vfs.KindCloud }
func (f *FS) Label() string  { return f.label }
func (f *FS) Root() string {
	if f.caps.home != "" {
		return f.caps.home
	}
	return "/"
}
func (f *FS) Account() Account { return f.acc }

func (f *FS) List(p string) ([]vfs.Entry, error) {
	p = clean(p)
	entries, err := f.b.list(f.ctx, p)
	return entries, pathErr("list", p, err)
}

func (f *FS) Stat(p string) (vfs.Entry, error) {
	p = clean(p)
	if p == "/" {
		return vfs.Entry{Name: "/", IsDir: true, Mode: os.ModeDir | 0o755}, nil
	}
	e, err := f.b.stat(f.ctx, p)
	return e, pathErr("stat", p, err)
}

func (f *FS) Mkdir(p string) error {
	p = clean(p)
	return pathErr("mkdir", p, f.b.mkdir(f.ctx, p))
}

func (f *FS) CreateEmptyFile(p string) error {
	p = clean(p)
	return pathErr("create", p, f.b.createEmpty(f.ctx, p))
}

func (f *FS) Remove(p string) error {
	p = clean(p)
	if p == "/" {
		return pathErr("remove", p, os.ErrPermission)
	}
	return pathErr("remove", p, f.b.remove(f.ctx, p))
}

func (f *FS) Rename(oldPath, newPath string) error {
	oldPath, newPath = clean(oldPath), clean(newPath)
	return pathErr("rename", oldPath, f.b.move(f.ctx, oldPath, newPath))
}

func (f *FS) Open(p string) (io.ReadCloser, error) {
	p = clean(p)
	r, err := f.b.download(f.ctx, p, 0)
	return r, pathErr("open", p, err)
}

// Create streams the content straight to the service where it accepts
// content of unknown size; otherwise it spools it to a temporary file
// first, uploading it on Close.
func (f *FS) Create(p string) (io.WriteCloser, error) {
	p = clean(p)
	if f.caps.streamUpload {
		return f.pipeUpload(p, -1), nil
	}
	tmp, err := os.CreateTemp("", "shfm-cloud-upload-*")
	if err != nil {
		return nil, err
	}
	return &spoolWriter{f: f, p: p, tmp: tmp, sent: &sentCounter{}}, nil
}

func (f *FS) createSized(p string, size int64) (io.WriteCloser, error) {
	if size < 0 {
		return f.Create(p)
	}
	return f.pipeUpload(clean(p), size), nil
}

// pipeUpload runs the upload in the background, fed by the returned
// writer; Close waits for it to finish.
func (f *FS) pipeUpload(p string, size int64) io.WriteCloser {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	sent := &sentCounter{}
	go func() {
		err := f.b.upload(withSentCounter(f.ctx, sent), p, size, pr)
		pr.CloseWithError(errOr(err, io.ErrClosedPipe))
		done <- err
	}()
	return &pipeWriter{pw: pw, done: done, p: p, sent: sent}
}

// errOr returns err, or fallback when err is nil (a reader closed with a
// nil error would make further writes look successful).
func errOr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

// pipeWriter and spoolWriter implement vfs.SentReporter: Write only
// hands the data over, to an upload that sends it in chunks, or on Close.
type pipeWriter struct {
	pw   *io.PipeWriter
	done chan error
	p    string
	sent *sentCounter
}

func (w *pipeWriter) Write(b []byte) (int, error) { return w.pw.Write(b) }
func (w *pipeWriter) Sent() int64                 { return w.sent.Sent() }

func (w *pipeWriter) Close() error {
	w.pw.Close()
	return pathErr("upload", w.p, <-w.done)
}

type spoolWriter struct {
	f    *FS
	p    string
	tmp  *os.File
	sent *sentCounter
}

func (w *spoolWriter) Write(b []byte) (int, error) { return w.tmp.Write(b) }
func (w *spoolWriter) Sent() int64                 { return w.sent.Sent() }

func (w *spoolWriter) Close() error {
	defer os.Remove(w.tmp.Name())
	defer w.tmp.Close()
	size, err := w.tmp.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = w.tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		return err
	}
	return pathErr("upload", w.p, w.f.b.upload(withSentCounter(w.f.ctx, w.sent), w.p, size, w.tmp))
}

func (f *FS) Join(elem ...string) string { return path.Join(elem...) }
func (f *FS) Dir(p string) string        { return path.Dir(p) }
func (f *FS) Base(p string) string       { return path.Base(p) }

// SupportsTrash is about the local Freedesktop trash: removing an entry
// moves it into the service's own trash anyway (see backend.remove and
// TrashName).
func (f *FS) SupportsTrash() bool { return false }

// TrashName implements vfs.ServiceTrash.
func (f *FS) TrashName() string {
	switch f.acc.Provider {
	case GoogleDrive:
		return "Google Drive trash"
	case Dropbox:
		return "Dropbox's deleted files"
	default:
		return "OneDrive recycle bin"
	}
}

func (f *FS) Close() error {
	f.cancel()
	return nil
}

// Redial implements vfs.Redialer: an independent FS for the same account,
// starting from the latest saved token.
func (f *FS) Redial() (vfs.FileSystem, error) { return Dial(f.acc) }

// Space implements vfs.SpaceReporter.
func (f *FS) Space(p string) (total, free uint64, err error) {
	return f.b.space(f.ctx)
}

func (f *FS) chtimes(p string, mtime time.Time) error {
	if mtime.IsZero() {
		return nil
	}
	p = clean(p)
	return pathErr("chtimes", p, f.b.setModTime(f.ctx, p, mtime))
}

// OpenRandom implements vfs.RandomAccessOpener, for the FUSE mount. A file
// opened read-only is read with ranged downloads, reusing the current one
// as long as the reads go on sequentially. A file opened for writing is
// worked on in a local temporary copy — no service can change a file in
// place — downloaded on the first read or write that needs the existing
// content, and uploaded on Close if it changed.
func (f *FS) OpenRandom(p string, flag int, perm os.FileMode) (vfs.RandomAccessFile, error) {
	p = clean(p)
	e, err := f.b.stat(f.ctx, p)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, pathErr("open", p, err)
	}
	if exists && e.IsDir {
		return nil, pathErr("open", p, syscall.EISDIR)
	}
	if flag&(os.O_WRONLY|os.O_RDWR) == 0 {
		if !exists {
			return nil, pathErr("open", p, os.ErrNotExist)
		}
		return &rangeReader{f: f, p: p}, nil
	}

	switch {
	case exists && flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		return nil, pathErr("open", p, os.ErrExist)
	case !exists && flag&os.O_CREATE == 0:
		return nil, pathErr("open", p, os.ErrNotExist)
	case !exists:
		// The file must exist as soon as open(2) returns.
		if err := f.b.createEmpty(f.ctx, p); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, pathErr("create", p, err)
		}
	}
	tmp, err := os.CreateTemp("", "shfm-cloud-file-*")
	if err != nil {
		return nil, err
	}
	w := &writeBackFile{f: f, p: p, tmp: tmp, loaded: !exists}
	if exists && flag&os.O_TRUNC != 0 {
		w.loaded, w.dirty = true, true
	}
	return w, nil
}

// rangeReader is a file opened read-only through OpenRandom.
type rangeReader struct {
	f *FS
	p string

	mu  sync.Mutex
	r   io.ReadCloser // the current download, positioned at pos
	pos int64
}

func (r *rangeReader) ReadAt(b []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.r == nil || r.pos != off {
		if r.r != nil {
			r.r.Close()
		}
		rc, err := r.f.b.download(r.f.ctx, r.p, off)
		if err != nil {
			r.r = nil
			return 0, pathErr("read", r.p, err)
		}
		r.r, r.pos = rc, off
	}
	n, err := io.ReadFull(r.r, b)
	r.pos += int64(n)
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		r.r.Close()
		r.r = nil
		return n, io.EOF
	}
	if err != nil {
		r.r.Close()
		r.r = nil
		return n, pathErr("read", r.p, err)
	}
	return n, nil
}

func (r *rangeReader) WriteAt(b []byte, off int64) (int, error) {
	return 0, pathErr("write", r.p, syscall.EBADF)
}

func (r *rangeReader) Truncate(size int64) error { return pathErr("truncate", r.p, syscall.EBADF) }

func (r *rangeReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.r != nil {
		r.r.Close()
		r.r = nil
	}
	return nil
}

// writeBackFile is a file opened for writing through OpenRandom.
type writeBackFile struct {
	f   *FS
	p   string
	tmp *os.File

	mu     sync.Mutex
	loaded bool // tmp holds the file's content
	dirty  bool // tmp changed since: upload it on Close
}

// loadLocked downloads the file's current content into tmp.
func (w *writeBackFile) loadLocked() error {
	if w.loaded {
		return nil
	}
	r, err := w.f.b.download(w.f.ctx, w.p, 0)
	if err != nil {
		return pathErr("read", w.p, err)
	}
	defer r.Close()
	if _, err := io.Copy(w.tmp, r); err != nil {
		return pathErr("read", w.p, err)
	}
	w.loaded = true
	return nil
}

func (w *writeBackFile) ReadAt(b []byte, off int64) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.loadLocked(); err != nil {
		return 0, err
	}
	return w.tmp.ReadAt(b, off)
}

func (w *writeBackFile) WriteAt(b []byte, off int64) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.loadLocked(); err != nil {
		return 0, err
	}
	w.dirty = true
	return w.tmp.WriteAt(b, off)
}

func (w *writeBackFile) Truncate(size int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if size == 0 {
		w.loaded = true // nothing of the old content is needed
	} else if err := w.loadLocked(); err != nil {
		return err
	}
	w.dirty = true
	return w.tmp.Truncate(size)
}

func (w *writeBackFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer os.Remove(w.tmp.Name())
	defer w.tmp.Close()
	if !w.dirty {
		return nil
	}
	w.dirty = false
	size, err := w.tmp.Seek(0, io.SeekEnd)
	if err == nil {
		_, err = w.tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		return err
	}
	return pathErr("upload", w.p, w.f.b.upload(w.f.ctx, w.p, size, w.tmp))
}

// fileEntry and dirEntry build the entries the backends report.
func fileEntry(name string, size int64, mod time.Time) vfs.Entry {
	return vfs.Entry{Name: name, Size: size, Mode: 0o644, ModTime: mod}
}

func dirEntry(name string, mod time.Time) vfs.Entry {
	return vfs.Entry{Name: name, IsDir: true, Mode: os.ModeDir | 0o755, ModTime: mod}
}

// errIsDir and errReadOnly are the answers to writing where it can't be
// done.
var (
	errIsDir    = syscall.EISDIR
	errNotDir   = syscall.ENOTDIR
	errReadOnly = &APIError{Status: 403, Message: "Google Docs documents are read-only in shfm", kind: os.ErrPermission}
)
