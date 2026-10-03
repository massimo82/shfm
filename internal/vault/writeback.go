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
	"errors"
	"io"
	"os"
	"sync"
	"syscall"

	"shfm/internal/vfs"
)

// openWriteBack opens p for writing through a working copy.
func (f *FS) openWriteBack(p string, flag int) (vfs.RandomAccessFile, error) {
	if _, _, err := f.st.keys(); err != nil {
		return nil, err
	}
	dir, ok := SpoolDir()
	if !ok {
		return nil, pathErr("open", p, syscall.EROFS)
	}
	e, err := f.Stat(p)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if exists && e.IsDir {
		return nil, pathErr("open", p, syscall.EISDIR)
	}
	switch {
	case exists && flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		return nil, pathErr("open", p, os.ErrExist)
	case !exists && flag&os.O_CREATE == 0:
		return nil, pathErr("open", p, os.ErrNotExist)
	case !exists:
		// The file must exist as soon as open(2) returns.
		if err := f.CreateEmptyFile(p); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	tmp, err := os.CreateTemp(dir, "vault-*")
	if err != nil {
		return nil, err
	}
	// Unlinked at once: the content lives as long as the open file, and
	// nothing is left behind even if shfm is killed.
	os.Remove(tmp.Name())
	w := &writeBackFile{f: f, p: p, tmp: tmp, loaded: !exists}
	if exists && flag&os.O_TRUNC != 0 {
		w.loaded, w.dirty = true, true
	}
	return w, nil
}

var _ vfs.WriteBacker = (*writeBackFile)(nil)

// writeBackFile is a vault file opened for writing: a decrypted working
// copy, loaded on the first access that needs the current content, and
// encrypted back into the vault on Close if it changed.
type writeBackFile struct {
	f   *FS
	p   string
	tmp *os.File

	mu     sync.Mutex
	loaded bool // tmp holds the file's content
	dirty  bool // tmp changed since: write it back on Close
}

func (w *writeBackFile) loadLocked() error {
	if w.loaded {
		return nil
	}
	r, err := w.f.Open(w.p)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := io.Copy(w.tmp, r); err != nil {
		return err
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

// WriteBack implements vfs.WriteBacker: the changes so far are encrypted
// into the vault now, the file staying open.
func (w *writeBackFile) WriteBack() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.syncLocked()
}

func (w *writeBackFile) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer w.tmp.Close()
	return w.syncLocked()
}

func (w *writeBackFile) syncLocked() error {
	if !w.dirty {
		return nil
	}
	w.dirty = false
	fi, err := w.tmp.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()
	dst, err := w.f.CreateSized(w.p, size)
	if err != nil {
		w.dirty = true
		return err
	}
	if _, err := io.Copy(dst, io.NewSectionReader(w.tmp, 0, size)); err != nil {
		dst.(*encryptWriter).Abort() // the vault keeps the previous content
		w.dirty = true
		return err
	}
	if err := dst.Close(); err != nil {
		w.dirty = true
		return err
	}
	return nil
}
