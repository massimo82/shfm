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

//go:build linux

package vfs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"shfm/internal/mtp"
)

// mtpDevice is the part of *mtp.Device MTPFS uses, so tests can substitute
// a fake device.
type mtpDevice interface {
	GetObjectHandles(parent uint32) ([]uint32, error)
	Mkdir(parent uint32, name string) (uint32, error)
	Space() (total, free uint64, err error)
	Close() error
	GetObjectInfo(handle uint32) (mtp.ObjectInfo, error)
	GetObjectReader(handle uint32) (io.ReadCloser, error)
	NewObjectWriter(parent uint32, name string, size int64) (io.WriteCloser, error)
	CreateEmpty(parent uint32, name string) (uint32, error)
	DeleteObject(handle uint32) error
	Rename(handle uint32, newName string) error
	CanRename() bool
	CanReadPartial() bool
	CanEditInPlace() bool
	ReadAt(handle uint32, p []byte, off int64) (int, error)
	BeginEdit(handle uint32) error
	EndEdit(handle uint32) error
	WriteAt(handle uint32, p []byte, off int64) (int, error)
	Truncate(handle uint32, size int64) error
}

// mtpFile is an MTP file opened for random access. It works in one of
// three ways, depending on what the device supports:
//
//   - reads of a range go straight to the device (GetPartialObject, or
//     Android's 64-bit variant), so a player streams a video from a phone;
//   - writes and truncation, on devices with Android's editing extensions,
//     go straight into the object too, between BeginEdit and EndEdit;
//   - otherwise the whole content is held in a local temp file (buf):
//     downloaded when opened, and — if changed — uploaded back when
//     closed, replacing the original object.
type mtpFile struct {
	fs     *MTPFS
	path   string
	handle uint32
	write  bool

	editing bool     // between BeginEdit and EndEdit
	buf     *os.File // the local buffer, if the content is held locally
	dirty   bool     // buf differs from the object on the device
}

// OpenRandom implements RandomAccessOpener. perm is ignored: MTP has no
// permission bits.
func (m *MTPFS) OpenRandom(path string, flag int, perm os.FileMode) (RandomAccessFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	handle, isDir, err := m.resolve(path)
	switch {
	case err == nil && isDir:
		return nil, syscall.EISDIR
	case err == nil && flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		return nil, os.ErrExist
	case errors.Is(err, os.ErrNotExist) && flag&os.O_CREATE != 0:
		parent, pIsDir, perr := m.resolve(m.Dir(path))
		if perr != nil {
			return nil, perr
		}
		if !pIsDir {
			return nil, syscall.ENOTDIR
		}
		if handle, err = m.dev.CreateEmpty(parent, m.Base(path)); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	}

	f := &mtpFile{fs: m, path: path, handle: handle, write: flag&(os.O_WRONLY|os.O_RDWR) != 0}
	trunc := flag&os.O_TRUNC != 0 && f.write
	switch {
	case f.write && m.dev.CanEditInPlace():
		if trunc {
			if err := f.beginEditLocked(); err != nil {
				return nil, err
			}
			if err := m.dev.Truncate(handle, 0); err != nil {
				m.dev.EndEdit(handle)
				return nil, err
			}
		}
	case f.write || !m.dev.CanReadPartial():
		if err := f.bufferLocked(!trunc); err != nil {
			return nil, err
		}
		f.dirty = trunc
	}
	return f, nil
}

// bufferLocked switches f to a local buffer, filled with the object's
// current content if download is true. m.mu must be held.
func (f *mtpFile) bufferLocked(download bool) error {
	tmp, err := os.CreateTemp("", "shfm-mtp-edit-*")
	if err != nil {
		return err
	}
	os.Remove(tmp.Name()) // unlinked: nothing is left behind, even on a crash
	if download {
		rc, err := f.fs.dev.GetObjectReader(f.handle)
		if err == nil {
			_, err = io.Copy(tmp, rc)
			if cerr := rc.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			tmp.Close()
			return err
		}
	}
	f.buf = tmp
	return nil
}

func (f *mtpFile) beginEditLocked() error {
	if f.editing {
		return nil
	}
	if err := f.fs.dev.BeginEdit(f.handle); err != nil {
		return err
	}
	f.editing = true
	return nil
}

func (f *mtpFile) ReadAt(p []byte, off int64) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if f.buf == nil {
		n, err := f.fs.dev.ReadAt(f.handle, p, off)
		if !errors.Is(err, mtp.ErrNotSupported) {
			return n, err
		}
		// A range the device can't read partially (past 4 GiB with only
		// the standard operation): fall back to the local buffer.
		if f.editing {
			return 0, err
		}
		if err := f.bufferLocked(true); err != nil {
			return 0, err
		}
	}
	return f.buf.ReadAt(p, off)
}

func (f *mtpFile) WriteAt(p []byte, off int64) (int, error) {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if !f.write {
		return 0, syscall.EBADF
	}
	if f.buf != nil {
		f.dirty = true
		return f.buf.WriteAt(p, off)
	}
	if err := f.beginEditLocked(); err != nil {
		return 0, err
	}
	return f.fs.dev.WriteAt(f.handle, p, off)
}

func (f *mtpFile) Truncate(size int64) error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	if !f.write {
		return syscall.EBADF
	}
	if f.buf != nil {
		f.dirty = true
		return f.buf.Truncate(size)
	}
	if err := f.beginEditLocked(); err != nil {
		return err
	}
	return f.fs.dev.Truncate(f.handle, size)
}

// Close commits an in-place edit, or uploads a changed local buffer.
func (f *mtpFile) Close() error {
	f.fs.mu.Lock()
	defer f.fs.mu.Unlock()
	var err error
	if f.editing {
		err = f.fs.dev.EndEdit(f.handle)
		f.editing = false
	}
	if f.buf != nil {
		if f.dirty {
			err = f.uploadLocked()
		}
		f.buf.Close()
		f.buf = nil
	}
	return err
}

// uploadLocked replaces the object with the buffer's content. MTP can't
// overwrite an object's data without the Android extensions, so the new
// content is uploaded as a new object first, under a temporary name, and
// only then does it take the original's place: a failed upload leaves the
// original untouched.
func (f *mtpFile) uploadLocked() error {
	m := f.fs
	size, err := f.buf.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err := f.buf.Seek(0, io.SeekStart); err != nil {
		return err
	}
	parent, _, err := m.resolve(m.Dir(f.path))
	if err != nil {
		return err
	}
	name := m.Base(f.path)

	if !m.dev.CanRename() {
		// No way to rename the upload into place: replace directly.
		if err := m.dev.DeleteObject(f.handle); err != nil {
			return err
		}
		return uploadFrom(m.dev, parent, name, f.buf, size)
	}
	tmpName := fmt.Sprintf(".%s.shfm-upload", name)
	if err := uploadFrom(m.dev, parent, tmpName, f.buf, size); err != nil {
		return err
	}
	tmpHandle, err := m.findChild(parent, tmpName)
	if err != nil {
		return err
	}
	if err := m.dev.DeleteObject(f.handle); err != nil {
		m.dev.DeleteObject(tmpHandle)
		return err
	}
	if err := m.dev.Rename(tmpHandle, name); err != nil {
		return fmt.Errorf("new content of %s saved as %s, but renaming it failed: %w", name, tmpName, err)
	}
	f.handle = tmpHandle
	return nil
}

func uploadFrom(dev mtpDevice, parent uint32, name string, r io.Reader, size int64) error {
	w, err := dev.NewObjectWriter(parent, name, size)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// findChild returns the handle of the child of parent named name.
func (m *MTPFS) findChild(parent uint32, name string) (uint32, error) {
	children, err := m.dev.GetObjectHandles(parent)
	if err != nil {
		return 0, err
	}
	for _, h := range children {
		if info, err := m.dev.GetObjectInfo(h); err == nil && info.Filename == name {
			return h, nil
		}
	}
	return 0, os.ErrNotExist
}
