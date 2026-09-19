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
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"shfm/internal/mtp"
)

// MTPFS exposes an MTP device (Android smartphone, camera, media
// player...) connected over USB through the common VFS interface, using
// exclusively the internal "shfm/internal/mtp" package (PTP/MTP protocol
// spoken directly over the Linux kernel's usbfs ioctls): no libusb, no
// cgo, no external command.
//
// VFS paths ("/DCIM/Camera/photo.jpg") are resolved into PTP object
// handles by walking from the root on every call: more USB round-trips
// than a persistent cache, but much simpler to keep correct (no cache
// invalidation to manage) and in any case negligible compared to the
// intrinsic latency of the USB bus and the device itself.
type MTPFS struct {
	mu    sync.Mutex
	dev   *mtp.Device
	label string
}

// DialMTP opens a session to the MTP device described by info.
func DialMTP(info mtp.DeviceInfo) (*MTPFS, error) {
	dev, err := mtp.Open(info)
	if err != nil {
		return nil, err
	}
	return &MTPFS{dev: dev, label: "mtp://" + info.Label()}, nil
}

func (m *MTPFS) Kind() Kind    { return KindMTP }
func (m *MTPFS) Label() string { return m.label }
func (m *MTPFS) Root() string  { return "/" }

// resolve walks from the root down to path, returning the handle of the
// corresponding object. The root itself (path=="/") is represented by the
// conventional handle mtp.RootHandle and doesn't correspond to a real PTP
// object.
func (m *MTPFS) resolve(path string) (handle uint32, isDir bool, err error) {
	segs := splitPath(path)
	cur := mtp.RootHandle
	isDir = true
	for _, seg := range segs {
		children, err := m.dev.GetObjectHandles(cur)
		if err != nil {
			return 0, false, err
		}
		found := false
		for _, h := range children {
			info, err := m.dev.GetObjectInfo(h)
			if err != nil {
				continue // unreadable object: skip it instead of failing the whole resolution
			}
			if info.Filename == seg {
				cur, isDir, found = h, info.ObjectFormat == mtp.FormatAssociation, true
				break
			}
		}
		if !found {
			return 0, false, os.ErrNotExist
		}
	}
	return cur, isDir, nil
}

func splitPath(path string) []string {
	var out []string
	for _, s := range strings.Split(path, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (m *MTPFS) List(path string) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	handle, isDir, err := m.resolve(path)
	if err != nil {
		return nil, err
	}
	if !isDir {
		return nil, fmt.Errorf("%s is not a folder", path)
	}
	children, err := m.dev.GetObjectHandles(handle)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(children))
	for _, h := range children {
		info, err := m.dev.GetObjectInfo(h)
		if err != nil {
			continue
		}
		entries = append(entries, Entry{
			Name:    info.Filename,
			IsDir:   info.ObjectFormat == mtp.FormatAssociation,
			Size:    int64(info.ObjectSize),
			ModTime: info.ModificationDate,
		})
	}
	return entries, nil
}

func (m *MTPFS) Stat(path string) (Entry, error) {
	if path == "/" {
		return Entry{Name: "/", IsDir: true}, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	handle, isDir, err := m.resolve(path)
	if err != nil {
		return Entry{}, err
	}
	info, err := m.dev.GetObjectInfo(handle)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Name: info.Filename, IsDir: isDir, Size: int64(info.ObjectSize), ModTime: info.ModificationDate}, nil
}

func (m *MTPFS) Mkdir(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, name := m.Dir(path), m.Base(path)
	parentHandle, isDir, err := m.resolve(parent)
	if err != nil {
		return err
	}
	if !isDir {
		return fmt.Errorf("%s is not a folder", parent)
	}
	_, err = m.dev.Mkdir(parentHandle, name)
	return err
}

func (m *MTPFS) CreateEmptyFile(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	parent, name := m.Dir(path), m.Base(path)
	parentHandle, isDir, err := m.resolve(parent)
	if err != nil {
		return err
	}
	if !isDir {
		return fmt.Errorf("%s is not a folder", parent)
	}
	w, err := m.dev.NewObjectWriter(parentHandle, name, 0)
	if err != nil {
		return err
	}
	return w.Close()
}

func (m *MTPFS) Remove(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	handle, _, err := m.resolve(path)
	if err != nil {
		return err
	}
	return m.dev.DeleteObject(handle)
}

func (m *MTPFS) Rename(oldPath, newPath string) error {
	if m.Dir(oldPath) != m.Dir(newPath) {
		// MTP rename (SetObjectPropValue) only changes the name, not the
		// parent: if this is also a move across folders, let fileops
		// handle it via copy+delete.
		return ErrNotSupported
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	handle, _, err := m.resolve(oldPath)
	if err != nil {
		return err
	}
	if err := m.dev.Rename(handle, m.Base(newPath)); err != nil {
		if err == mtp.ErrNotSupported {
			return ErrNotSupported
		}
		return err
	}
	return nil
}

func (m *MTPFS) Open(path string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	handle, isDir, err := m.resolve(path)
	if err != nil {
		return nil, err
	}
	if isDir {
		return nil, fmt.Errorf("%s is a folder", path)
	}
	// NOTE: the mutex stays locked for the whole duration of the streaming
	// download, because the PTP bus/session only supports one transaction
	// at a time. It is unlocked when the reader is closed.
	rc, err := m.dev.GetObjectReader(handle)
	if err != nil {
		return nil, err
	}
	return &unlockingReadCloser{ReadCloser: rc, unlock: m.mu.Unlock}, nil
}

func (m *MTPFS) Create(path string) (io.WriteCloser, error) {
	// The size isn't known upfront: we buffer to a temp file and determine
	// it at Close(), then perform the real MTP upload (which requires
	// declaring the size before the data phase). When the size is already
	// known (the common case for copies), fileops instead uses CreateSized,
	// which avoids disk buffering entirely.
	tmp, err := os.CreateTemp("", "shfm-mtp-upload-*")
	if err != nil {
		return nil, err
	}
	return &bufferedUpload{fs: m, path: path, tmp: tmp}, nil
}

// CreateSized implements vfs.SizedCreator: when the source size is
// already known (the normal case in a copy), it avoids the temp file and
// streams directly to the device.
func (m *MTPFS) CreateSized(path string, size int64) (io.WriteCloser, error) {
	m.mu.Lock()
	parent, name := m.Dir(path), m.Base(path)
	parentHandle, isDir, err := m.resolve(parent)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if !isDir {
		m.mu.Unlock()
		return nil, fmt.Errorf("%s is not a folder", parent)
	}
	w, err := m.dev.NewObjectWriter(parentHandle, name, size)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	return &unlockingWriteCloser{WriteCloser: w, unlock: m.mu.Unlock}, nil
}

func (m *MTPFS) Join(elem ...string) string {
	clean := make([]string, 0, len(elem))
	for _, e := range elem {
		e = strings.Trim(e, "/")
		if e != "" {
			clean = append(clean, e)
		}
	}
	return "/" + strings.Join(clean, "/")
}

func (m *MTPFS) Dir(path string) string {
	path = strings.TrimSuffix(path, "/")
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return "/"
	}
	return path[:idx]
}

func (m *MTPFS) Base(path string) string {
	path = strings.TrimSuffix(path, "/")
	idx := strings.LastIndex(path, "/")
	return path[idx+1:]
}

func (m *MTPFS) SupportsTrash() bool { return false }

func (m *MTPFS) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dev.Close()
}

// unlockingReadCloser releases the MTP session's mutex on close, letting
// other operations resume only once the download has finished (the USB
// bus and the PTP session don't support concurrent transactions).
type unlockingReadCloser struct {
	io.ReadCloser
	unlock func()
	once   sync.Once
}

func (u *unlockingReadCloser) Close() error {
	err := u.ReadCloser.Close()
	u.once.Do(u.unlock)
	return err
}

type unlockingWriteCloser struct {
	io.WriteCloser
	unlock func()
	once   sync.Once
}

func (u *unlockingWriteCloser) Close() error {
	err := u.WriteCloser.Close()
	u.once.Do(u.unlock)
	return err
}

// bufferedUpload implements io.WriteCloser by buffering to a temp file
// until the final size is known (at Close), then performs the real MTP
// upload streaming from that file and deletes the temp file.
type bufferedUpload struct {
	fs   *MTPFS
	path string
	tmp  *os.File
	err  error
}

func (b *bufferedUpload) Write(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	n, err := b.tmp.Write(p)
	if err != nil {
		b.err = err
	}
	return n, err
}

func (b *bufferedUpload) Close() error {
	defer os.Remove(b.tmp.Name())
	if b.err != nil {
		b.tmp.Close()
		return b.err
	}
	info, err := b.tmp.Stat()
	if err != nil {
		b.tmp.Close()
		return err
	}
	size := info.Size()
	if _, err := b.tmp.Seek(0, io.SeekStart); err != nil {
		b.tmp.Close()
		return err
	}
	defer b.tmp.Close()

	w, err := b.fs.CreateSized(b.path, size)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, b.tmp); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
