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
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"shfm/internal/mtp"
)

// fakeMTP is an in-memory MTP device, with configurable support for the
// optional operations.
type fakeMTP struct {
	objects map[uint32]*fakeObject
	next    uint32

	partial     bool  // supports partial reads
	partialMax  int64 // partial reads only below this offset (0: unlimited)
	edit        bool  // supports Android's in-place editing
	canRename   bool
	downloads   int // whole-object downloads (GetObjectReader)
	editingOpen map[uint32]bool
}

type fakeObject struct {
	parent uint32
	name   string
	dir    bool
	data   []byte
}

func newFakeMTP() *fakeMTP {
	return &fakeMTP{objects: map[uint32]*fakeObject{}, next: 1, canRename: true, editingOpen: map[uint32]bool{}}
}

func (d *fakeMTP) add(parent uint32, name string, dir bool, data []byte) uint32 {
	h := d.next
	d.next++
	d.objects[h] = &fakeObject{parent: parent, name: name, dir: dir, data: data}
	return h
}

func (d *fakeMTP) GetObjectHandles(parent uint32) ([]uint32, error) {
	var out []uint32
	for h := uint32(1); h < d.next; h++ {
		if o, ok := d.objects[h]; ok && o.parent == parent {
			out = append(out, h)
		}
	}
	return out, nil
}

func (d *fakeMTP) GetObjectInfo(h uint32) (mtp.ObjectInfo, error) {
	o, ok := d.objects[h]
	if !ok {
		return mtp.ObjectInfo{}, fmt.Errorf("no object %d", h)
	}
	format := uint16(0x3000)
	if o.dir {
		format = mtp.FormatAssociation
	}
	return mtp.ObjectInfo{Filename: o.name, ObjectFormat: format, ObjectSize: uint64(len(o.data))}, nil
}

func (d *fakeMTP) GetObjectReader(h uint32) (io.ReadCloser, error) {
	d.downloads++
	return io.NopCloser(bytes.NewReader(append([]byte(nil), d.objects[h].data...))), nil
}

type fakeUpload struct {
	d      *fakeMTP
	parent uint32
	name   string
	buf    bytes.Buffer
}

func (u *fakeUpload) Write(p []byte) (int, error) { return u.buf.Write(p) }
func (u *fakeUpload) Close() error {
	u.d.add(u.parent, u.name, false, u.buf.Bytes())
	return nil
}

func (d *fakeMTP) NewObjectWriter(parent uint32, name string, size int64) (io.WriteCloser, error) {
	return &fakeUpload{d: d, parent: parent, name: name}, nil
}

func (d *fakeMTP) CreateEmpty(parent uint32, name string) (uint32, error) {
	return d.add(parent, name, false, nil), nil
}

func (d *fakeMTP) Mkdir(parent uint32, name string) (uint32, error) {
	return d.add(parent, name, true, nil), nil
}

func (d *fakeMTP) DeleteObject(h uint32) error {
	delete(d.objects, h)
	return nil
}

func (d *fakeMTP) Rename(h uint32, name string) error {
	if !d.canRename {
		return mtp.ErrNotSupported
	}
	d.objects[h].name = name
	return nil
}

func (d *fakeMTP) CanRename() bool      { return d.canRename }
func (d *fakeMTP) CanReadPartial() bool { return d.partial }
func (d *fakeMTP) CanEditInPlace() bool { return d.edit }

func (d *fakeMTP) ReadAt(h uint32, p []byte, off int64) (int, error) {
	if !d.partial || (d.partialMax > 0 && off+int64(len(p)) > d.partialMax) {
		return 0, mtp.ErrNotSupported
	}
	data := d.objects[h].data
	if off >= int64(len(data)) {
		return 0, io.EOF
	}
	n := copy(p, data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (d *fakeMTP) BeginEdit(h uint32) error { d.editingOpen[h] = true; return nil }
func (d *fakeMTP) EndEdit(h uint32) error   { delete(d.editingOpen, h); return nil }

func (d *fakeMTP) WriteAt(h uint32, p []byte, off int64) (int, error) {
	if !d.editingOpen[h] {
		return 0, errors.New("write outside an edit")
	}
	o := d.objects[h]
	if end := off + int64(len(p)); end > int64(len(o.data)) {
		o.data = append(o.data, make([]byte, end-int64(len(o.data)))...)
	}
	copy(o.data[off:], p)
	return len(p), nil
}

func (d *fakeMTP) Truncate(h uint32, size int64) error {
	if !d.editingOpen[h] {
		return errors.New("truncate outside an edit")
	}
	o := d.objects[h]
	if size <= int64(len(o.data)) {
		o.data = o.data[:size]
	} else {
		o.data = append(o.data, make([]byte, size-int64(len(o.data)))...)
	}
	return nil
}

func (d *fakeMTP) Space() (uint64, uint64, error) { return 64 << 30, 10 << 30, nil }
func (d *fakeMTP) Close() error                   { return nil }

// content returns the data of the file named name in folder parent, and
// how many objects have that name.
func (d *fakeMTP) content(parent uint32, name string) ([]byte, int) {
	var data []byte
	n := 0
	for _, o := range d.objects {
		if o.parent == parent && o.name == name {
			data, n = o.data, n+1
		}
	}
	return data, n
}

func newTestMTPFS(dev *fakeMTP) *MTPFS { return &MTPFS{dev: dev, label: "mtp://fake"} }

func TestMTPPartialReadDoesNotDownload(t *testing.T) {
	dev := newFakeMTP()
	dev.partial = true
	dcim := dev.add(mtp.RootHandle, "DCIM", true, nil)
	dev.add(dcim, "video.mp4", false, []byte("0123456789abcdef"))
	fs := newTestMTPFS(dev)

	f, err := fs.OpenRandom("/DCIM/video.mp4", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if n, err := f.ReadAt(buf, 10); err != nil || string(buf[:n]) != "abcd" {
		t.Fatalf("ReadAt = %q, %v", buf[:n], err)
	}
	if n, err := f.ReadAt(buf, 14); err != io.EOF || string(buf[:n]) != "ef" {
		t.Fatalf("ReadAt at the end = %q, %v; want \"ef\", EOF", buf[:n], err)
	}
	f.Close()
	if dev.downloads != 0 {
		t.Fatalf("%d whole-object downloads, want 0", dev.downloads)
	}
}

func TestMTPPartialReadFallsBackBeyondLimit(t *testing.T) {
	dev := newFakeMTP()
	dev.partial, dev.partialMax = true, 8 // like the standard 32-bit operation
	dev.add(mtp.RootHandle, "big.mkv", false, []byte("0123456789abcdef"))
	fs := newTestMTPFS(dev)

	f, err := fs.OpenRandom("/big.mkv", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 4)
	if n, err := f.ReadAt(buf, 2); err != nil || string(buf[:n]) != "2345" {
		t.Fatalf("ReadAt below the limit = %q, %v", buf[:n], err)
	}
	if n, err := f.ReadAt(buf, 12); err != nil || string(buf[:n]) != "cdef" {
		t.Fatalf("ReadAt beyond the limit = %q, %v", buf[:n], err)
	}
	if dev.downloads != 1 {
		t.Fatalf("%d downloads, want 1 (fallback to the local buffer)", dev.downloads)
	}
}

func TestMTPInPlaceEdit(t *testing.T) {
	dev := newFakeMTP()
	dev.partial, dev.edit = true, true
	h := dev.add(mtp.RootHandle, "notes.txt", false, []byte("hello world"))
	fs := newTestMTPFS(dev)

	f, err := fs.OpenRandom("/notes.txt", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("J"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(5); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := string(dev.objects[h].data); got != "Jello" {
		t.Fatalf("content = %q, want %q", got, "Jello")
	}
	if dev.editingOpen[h] {
		t.Fatal("edit not ended at Close")
	}
	if dev.downloads != 0 {
		t.Fatalf("%d downloads, want 0 (edited in place)", dev.downloads)
	}

	// O_TRUNC in place.
	f, err = fs.OpenRandom("/notes.txt", os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := string(dev.objects[h].data); got != "" {
		t.Fatalf("after O_TRUNC, content = %q", got)
	}
}

func TestMTPBufferedEditReplacesObject(t *testing.T) {
	for _, canRename := range []bool{true, false} {
		t.Run(fmt.Sprintf("rename=%v", canRename), func(t *testing.T) {
			dev := newFakeMTP()
			dev.partial = true // but no in-place editing
			dev.canRename = canRename
			dir := dev.add(mtp.RootHandle, "Documents", true, nil)
			dev.add(dir, "notes.txt", false, []byte("hello world"))
			fs := newTestMTPFS(dev)

			f, err := fs.OpenRandom("/Documents/notes.txt", os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 5)
			if n, err := f.ReadAt(buf, 6); err != nil || string(buf[:n]) != "world" {
				t.Fatalf("ReadAt from the buffer = %q, %v", buf[:n], err)
			}
			if _, err := f.WriteAt([]byte("there"), 6); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			data, n := dev.content(dir, "notes.txt")
			if n != 1 || string(data) != "hello there" {
				t.Fatalf("after Close: %d object(s), content %q", n, data)
			}
			for _, o := range dev.objects {
				if strings.Contains(o.name, "shfm-upload") {
					t.Fatalf("temporary upload %q left behind", o.name)
				}
			}
		})
	}
}

func TestMTPBufferedCreateAndUnchangedClose(t *testing.T) {
	dev := newFakeMTP()
	h := dev.add(mtp.RootHandle, "keep.bin", false, []byte("data"))
	fs := newTestMTPFS(dev)

	// Opened for writing but never written: nothing is uploaded.
	f, err := fs.OpenRandom("/keep.bin", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, ok := dev.objects[h]; !ok {
		t.Fatal("unchanged file was replaced")
	}

	if _, err := fs.OpenRandom("/keep.bin", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0); !errors.Is(err, os.ErrExist) {
		t.Fatalf("O_EXCL on an existing file = %v, want ErrExist", err)
	}
	if _, err := fs.OpenRandom("/missing.bin", os.O_RDONLY, 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening a missing file = %v, want ErrNotExist", err)
	}

	f, err = fs.OpenRandom("/new.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("fresh"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if data, n := dev.content(mtp.RootHandle, "new.txt"); n != 1 || string(data) != "fresh" {
		t.Fatalf("new file: %d object(s), content %q", n, data)
	}
}

func TestMTPRenameReplacesDestination(t *testing.T) {
	dev := newFakeMTP()
	dev.add(mtp.RootHandle, "doc.txt", false, []byte("old"))
	dev.add(mtp.RootHandle, "doc.txt.tmp", false, []byte("new"))
	fs := newTestMTPFS(dev)

	if err := fs.Rename("/doc.txt.tmp", "/doc.txt"); err != nil {
		t.Fatal(err)
	}
	if data, n := dev.content(mtp.RootHandle, "doc.txt"); n != 1 || string(data) != "new" {
		t.Fatalf("after rename: %d object(s) named doc.txt, content %q", n, data)
	}

	// A device that can't rename must not lose the destination.
	dev.canRename = false
	dev.add(mtp.RootHandle, "other.tmp", false, []byte("x"))
	if err := fs.Rename("/other.tmp", "/doc.txt"); err != ErrNotSupported {
		t.Fatalf("rename without device support = %v, want ErrNotSupported", err)
	}
	if data, _ := dev.content(mtp.RootHandle, "doc.txt"); string(data) != "new" {
		t.Fatalf("destination changed to %q", data)
	}
}
