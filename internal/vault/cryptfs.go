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

//go:build vault

package vault

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"syscall"
	"time"

	"filippo.io/age"

	"shfm/internal/vfs"
)

// FS is an unlocked vault's content as a vfs.FileSystem (CryptFS): a
// decorator over the backend holding the vault, encrypting what is
// written and decrypting what is read. Its paths are rooted at "/", the
// vault's folder. Safe for concurrent use as far as the backend is.
type FS struct {
	st    *state
	inner vfs.FileSystem
	lay   layout
	owns  bool // Close closes inner: a connection the vault opened itself (redial)
}

var (
	_ vfs.FileSystem         = (*FS)(nil)
	_ vfs.SizedCreator       = (*FS)(nil)
	_ vfs.RandomAccessOpener = (*FS)(nil)
	_ vfs.TimesSetter        = (*FS)(nil)
	_ vfs.SpaceReporter      = (*FS)(nil)
	_ vfs.SourceKeyer        = (*FS)(nil)
)

// newFS returns the vault's FS over inner, implementing the optional
// interfaces whose presence depends on the backend's (see caps.go).
func newFS(st *state, inner vfs.FileSystem, owns bool) vfs.FileSystem {
	f := &FS{st: st, inner: inner, owns: owns}
	if st.cfg.Names == NamesPlain {
		f.lay = &plainLayout{st: st, fs: inner}
	} else {
		f.lay = &indexLayout{st: st, fs: inner}
	}
	return withCaps(f)
}

func clean(p string) string { return path.Clean("/" + p) }

func (f *FS) Kind() vfs.Kind { return vfs.KindVault }
func (f *FS) Label() string  { return f.st.label }
func (f *FS) Root() string   { return "/" }

// SourceKey implements vfs.SourceKeyer: two vaults may share a folder name
// (their Label), never a backend and folder.
func (f *FS) SourceKey() string {
	return "vault|" + f.inner.Kind().String() + "|" + f.inner.Label() + "|" + f.st.dir
}

// Backend returns the file system the vault is stored on.
func (f *FS) Backend() vfs.FileSystem { return f.inner }

func (f *FS) List(p string) ([]vfs.Entry, error) { return f.lay.list(clean(p)) }
func (f *FS) Stat(p string) (vfs.Entry, error)   { return f.lay.stat(clean(p)) }
func (f *FS) Mkdir(p string) error               { return f.lay.mkdir(clean(p)) }
func (f *FS) Remove(p string) error              { return f.lay.remove(clean(p)) }

// Rename renames or moves within the vault: vfs.ErrNotSupported when the
// backend can't move between folders, for the caller to copy instead.
func (f *FS) Rename(oldPath, newPath string) error {
	return f.lay.rename(clean(oldPath), clean(newPath))
}

func (f *FS) CreateEmptyFile(p string) error {
	p = clean(p)
	if _, err := f.Stat(p); err == nil {
		return pathErr("create", p, os.ErrExist)
	}
	w, err := f.Create(p)
	if err != nil {
		return err
	}
	return w.Close()
}

// Open decrypts file p as it is read. A failed integrity check — damaged
// or tampered content — is an error wrapping ErrDamaged, never silently
// wrong data: reading stops there.
func (f *FS) Open(p string) (io.ReadCloser, error) {
	p = clean(p)
	id, _, err := f.st.keys()
	if err != nil {
		return nil, err
	}
	b, err := f.lay.blob(p)
	if err != nil {
		return nil, err
	}
	r, err := f.inner.Open(b)
	if err != nil {
		return nil, err
	}
	src := &trackedReader{r: r}
	dr, err := age.Decrypt(src, id)
	if err != nil {
		r.Close()
		if src.err != nil {
			return nil, src.err
		}
		return nil, pathErr("open", p, fmt.Errorf("%w: %v", ErrDamaged, err))
	}
	return &decryptReader{p: p, dr: dr, src: src}, nil
}

// trackedReader remembers the backend's own read error, to tell it apart
// from a decryption failure.
type trackedReader struct {
	r   io.ReadCloser
	err error
}

func (t *trackedReader) Read(b []byte) (int, error) {
	n, err := t.r.Read(b)
	if err != nil && err != io.EOF {
		t.err = err
	}
	return n, err
}

type decryptReader struct {
	p   string
	dr  io.Reader
	src *trackedReader
}

func (d *decryptReader) Read(b []byte) (int, error) {
	n, err := d.dr.Read(b)
	if err != nil && err != io.EOF {
		if d.src.err != nil {
			return n, d.src.err
		}
		return n, pathErr("read", d.p, fmt.Errorf("%w: %v", ErrDamaged, err))
	}
	return n, err
}

func (d *decryptReader) Close() error { return d.src.r.Close() }

// Create encrypts what is written into file p, which only becomes p's
// content once the writer is closed: until then a previous p is intact,
// and a failed write leaves nothing behind.
func (f *FS) Create(p string) (io.WriteCloser, error) { return f.create(clean(p), -1) }

// CreateSized implements vfs.SizedCreator: for a backend that needs the
// final size before the data (MTP), the encrypted size follows from size;
// on the others it is Create. Closing after writing other than size bytes
// fails.
func (f *FS) CreateSized(p string, size int64) (io.WriteCloser, error) {
	return f.create(clean(p), size)
}

func (f *FS) create(p string, size int64) (io.WriteCloser, error) {
	_, rcpt, err := f.st.keys()
	if err != nil {
		return nil, err
	}
	b, commit, abort, err := f.lay.beginWrite(p)
	if err != nil {
		return nil, err
	}
	// The header and nonce go to a buffer first: once their length is
	// known, so is the encrypted size a sized backend needs.
	sw := &switchWriter{w: &bytes.Buffer{}}
	enc, err := age.Encrypt(sw, rcpt)
	if err != nil {
		return nil, err
	}
	head := sw.w.(*bytes.Buffer).Bytes()
	var dst io.WriteCloser
	sc, sized := f.inner.(vfs.SizedCreator)
	if sized && size >= 0 {
		dst, err = sc.CreateSized(b, cipherSize(int64(len(head)), size))
	} else {
		dst, err = f.inner.Create(b)
	}
	if err != nil {
		return nil, err
	}
	if _, err := dst.Write(head); err != nil {
		dst.Close()
		abort()
		return nil, err
	}
	sw.w = dst
	return &encryptWriter{p: p, enc: enc, dst: dst, want: size, commit: commit, abort: abort}, nil
}

// switchWriter writes to w, which can change between writes.
type switchWriter struct{ w io.Writer }

func (s *switchWriter) Write(b []byte) (int, error) { return s.w.Write(b) }

type encryptWriter struct {
	p      string
	enc    io.WriteCloser
	dst    io.WriteCloser
	n      int64
	want   int64 // the size declared to CreateSized, -1 if none
	commit func(int64) error
	abort  func()
	done   bool
}

func (w *encryptWriter) Write(b []byte) (int, error) {
	if w.done {
		return 0, os.ErrClosed
	}
	n, err := w.enc.Write(b)
	w.n += int64(n)
	return n, err
}

// Abort drops what was written: the file keeps its previous content, or
// isn't created.
func (w *encryptWriter) Abort() {
	if w.done {
		return
	}
	w.done = true
	w.enc.Close()
	w.dst.Close()
	w.abort()
}

func (w *encryptWriter) Close() error {
	if w.done {
		return os.ErrClosed
	}
	w.done = true
	err := w.enc.Close()
	if cerr := w.dst.Close(); err == nil {
		err = cerr
	}
	if err == nil && w.want >= 0 && w.n != w.want {
		err = fmt.Errorf("wrote %d bytes, %d declared", w.n, w.want)
	}
	if err == nil {
		err = w.commit(w.n)
	}
	if err != nil {
		w.abort()
		return pathErr("write", w.p, err)
	}
	return nil
}

func (f *FS) Join(elem ...string) string { return path.Join(elem...) }
func (f *FS) Dir(p string) string        { return path.Dir(p) }
func (f *FS) Base(p string) string       { return path.Base(p) }

// SupportsTrash: false. A vault's files never go to the local trash —
// encrypted under a random name there, they could only be restored by
// hand; deleting from a vault is final.
func (f *FS) SupportsTrash() bool { return false }

// Close closes the backend's connection if the vault opened it (a FUSE
// mount's, see redial); the backend the vault was opened on belongs to
// whoever opened it. The vault stays unlocked either way.
func (f *FS) Close() error {
	if f.owns {
		return f.inner.Close()
	}
	return nil
}

// Chtimes implements vfs.TimesSetter: only the modification time is kept.
func (f *FS) Chtimes(p string, atime, mtime time.Time) error {
	return f.lay.chtimes(clean(p), mtime)
}

// Space implements vfs.SpaceReporter, the backend's.
func (f *FS) Space(p string) (total, free uint64, err error) {
	if sr, ok := f.inner.(vfs.SpaceReporter); ok {
		return sr.Space(f.st.dir)
	}
	return 0, 0, vfs.ErrNotSupported
}

// OpenRandom implements vfs.RandomAccessOpener, for the FUSE mount through
// which external applications open the vault's files.
//
// Read-only, an age file is made of independently authenticated 64 KiB
// chunks, so a read at any offset only fetches and decrypts the chunks it
// covers (age's DecryptReaderAt).
//
// An age file can't be changed in place, so a file opened for writing is
// worked on in a working copy, re-encrypted into the vault on Close if it
// changed (see writeBackFile). The copy is decrypted content: it is only
// ever kept in $XDG_RUNTIME_DIR, a tmpfs in memory, and unlinked as soon as
// it's created; without $XDG_RUNTIME_DIR, writing fails with EROFS rather
// than put clear text on a disk.
func (f *FS) OpenRandom(p string, flag int, perm os.FileMode) (vfs.RandomAccessFile, error) {
	p = clean(p)
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return f.openWriteBack(p, flag)
	}
	return f.openReader(p)
}

func (f *FS) openReader(p string) (vfs.RandomAccessFile, error) {
	id, _, err := f.st.keys()
	if err != nil {
		return nil, err
	}
	b, err := f.lay.blob(p)
	if err != nil {
		return nil, err
	}
	e, err := f.inner.Stat(b)
	if err != nil {
		return nil, err
	}
	var src io.ReaderAt
	var closer io.Closer
	if ra, ok := f.inner.(vfs.RandomAccessOpener); ok {
		rf, err := ra.OpenRandom(b, os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		src, closer = rf, rf
	} else {
		r, err := f.inner.Open(b)
		if err != nil {
			return nil, err
		}
		at, ok := r.(io.ReaderAt) // the local backend's *os.File
		if !ok {
			r.Close()
			return nil, vfs.ErrNotSupported
		}
		src, closer = at, r
	}
	pr, size, err := age.DecryptReaderAt(src, e.Size, id)
	if err != nil {
		closer.Close()
		return nil, pathErr("open", p, fmt.Errorf("%w: %v", ErrDamaged, err))
	}
	return &randomFile{p: p, r: pr, size: size, c: closer}, nil
}

type randomFile struct {
	p    string
	r    io.ReaderAt
	size int64
	c    io.Closer
}

func (r *randomFile) ReadAt(b []byte, off int64) (int, error) {
	if off >= r.size {
		return 0, io.EOF
	}
	n, err := r.r.ReadAt(b, off)
	if err != nil && err != io.EOF {
		return n, pathErr("read", r.p, fmt.Errorf("%w: %v", ErrDamaged, err))
	}
	return n, err
}

func (r *randomFile) WriteAt(b []byte, off int64) (int, error) {
	return 0, pathErr("write", r.p, syscall.EROFS)
}

func (r *randomFile) Truncate(size int64) error { return pathErr("truncate", r.p, syscall.EROFS) }
func (r *randomFile) Close() error              { return r.c.Close() }

// dirSize implements vfs.DirSizer over the vault's own listing.
func (f *FS) dirSize(p string) (size, count int64, err error) {
	entries, err := f.List(p)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		count++
		if e.IsDir {
			s, c, err := f.dirSize(path.Join(clean(p), e.Name))
			if err != nil {
				if errors.Is(err, ErrLocked) {
					return 0, 0, err
				}
				continue // best effort, like the local backend
			}
			size, count = size+s, count+c
		} else {
			size += e.Size
		}
	}
	return size, count, nil
}

// redial implements vfs.Redialer: the same unlocked vault, over a new
// connection to the backend (the same local backend, which has none).
func (f *FS) redial() (vfs.FileSystem, error) {
	inner, owns := f.inner, false
	if rd, ok := f.inner.(vfs.Redialer); ok {
		var err error
		if inner, err = rd.Redial(); err != nil {
			return nil, err
		}
		owns = true
	}
	if f.st.locked.Load() {
		if owns {
			inner.Close()
		}
		return nil, ErrLocked
	}
	return newFS(f.st, inner, owns), nil
}
