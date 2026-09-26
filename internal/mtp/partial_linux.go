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

package mtp

import (
	"bytes"
	"io"
	"math"

	extmtp "github.com/hanwen/go-mtpfs/mtp"
)

// Operation codes for partial transfers and in-place editing: the standard
// PTP GetPartialObject (32-bit offsets), and Android's MTP extensions
// (64-bit offsets, plus writing into an existing object).
const (
	opGetPartialObject          = extmtp.OC_GetPartialObject // 0x101B
	opAndroidGetPartialObject64 = 0x95C1
	opAndroidSendPartialObject  = 0x95C2
	opAndroidTruncateObject     = 0x95C3
	opAndroidBeginEditObject    = 0x95C4
	opAndroidEndEditObject      = 0x95C5
)

// CanReadPartial reports whether the device can read a range of an object
// without transferring all of it.
func (d *Device) CanReadPartial() bool {
	return d.ops[opAndroidGetPartialObject64] || d.ops[opGetPartialObject]
}

// CanEditInPlace reports whether the device supports Android's extensions
// to write into, and truncate, an existing object.
func (d *Device) CanEditInPlace() bool {
	return d.ops[opAndroidBeginEditObject] && d.ops[opAndroidSendPartialObject] &&
		d.ops[opAndroidTruncateObject] && d.ops[opAndroidEndEditObject]
}

// sliceWriter collects a transaction's data phase into a caller's buffer.
type sliceWriter struct {
	buf []byte
	n   int
}

func (w *sliceWriter) Write(p []byte) (int, error) {
	c := copy(w.buf[w.n:], p)
	w.n += c
	if c < len(p) {
		return c, io.ErrShortBuffer
	}
	return len(p), nil
}

// ReadAt reads the range of object handle starting at off into p, like
// io.ReaderAt (fewer bytes and io.EOF at the end of the object). Returns
// ErrNotSupported if the device can't read that range partially (see
// CanReadPartial; the standard operation only reaches the first 4 GiB).
func (d *Device) ReadAt(handle uint32, p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w := &sliceWriter{buf: p}
	var err error
	switch {
	case d.ops[opAndroidGetPartialObject64]:
		err = d.dev.AndroidGetPartialObject64(handle, w, off, uint32(len(p)))
	case d.ops[opGetPartialObject] && off+int64(len(p)) <= math.MaxUint32:
		// go-mtpfs's own GetPartialObject sends the Android opcode with
		// the standard operation's parameters: build the request here.
		var req, rep extmtp.Container
		req.Code = opGetPartialObject
		req.Param = []uint32{handle, uint32(off), uint32(len(p))}
		err = d.dev.RunTransaction(&req, &rep, w, nil, 0)
	default:
		return 0, ErrNotSupported
	}
	if err != nil {
		return w.n, err
	}
	if w.n < len(p) {
		return w.n, io.EOF
	}
	return w.n, nil
}

// BeginEdit starts an in-place edit of object handle (see CanEditInPlace):
// WriteAt and Truncate are only valid between BeginEdit and EndEdit.
func (d *Device) BeginEdit(handle uint32) error { return d.dev.AndroidBeginEditObject(handle) }

// EndEdit commits the changes made since BeginEdit.
func (d *Device) EndEdit(handle uint32) error { return d.dev.AndroidEndEditObject(handle) }

// WriteAt writes p into object handle at off, during an in-place edit.
func (d *Device) WriteAt(handle uint32, p []byte, off int64) (int, error) {
	if err := d.dev.AndroidSendPartialObject(handle, off, uint32(len(p)), bytes.NewReader(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Truncate truncates or extends object handle to size bytes, during an
// in-place edit.
func (d *Device) Truncate(handle uint32, size int64) error {
	return d.dev.AndroidTruncate(handle, size)
}

// sendObjectInfo declares a new file object of the given size as a child
// of parent, returning its handle; its content must follow with SendObject.
func (d *Device) sendObjectInfo(parent uint32, name string, size int64) (uint32, error) {
	oi := extmtp.ObjectInfo{
		ObjectFormat: guessObjectFormat(name),
		ParentObject: parent,
		Filename:     name,
	}
	if size >= sizeUnknown {
		oi.CompressedSize = sizeUnknown
	} else {
		oi.CompressedSize = uint32(size)
	}
	_, _, handle, err := d.dev.SendObjectInfo(d.storageID, parent, &oi)
	return handle, err
}

// CreateEmpty creates an empty file named name as a child of parent and
// returns its handle.
func (d *Device) CreateEmpty(parent uint32, name string) (uint32, error) {
	handle, err := d.sendObjectInfo(parent, name, 0)
	if err != nil {
		return 0, err
	}
	if err := d.dev.SendObject(bytes.NewReader(nil), 0); err != nil {
		return 0, err
	}
	return handle, nil
}

// Space returns the capacity and free space, in bytes, of the storage in
// use.
func (d *Device) Space() (total, free uint64, err error) {
	var info extmtp.StorageInfo
	if err := d.dev.GetStorageInfo(d.storageID, &info); err != nil {
		return 0, 0, err
	}
	return info.MaxCapability, info.FreeSpaceInBytes, nil
}

// CanRename reports whether the device supports renaming objects (setting
// their ObjectFileName property). Assumed true when the device didn't say
// which operations it supports.
func (d *Device) CanRename() bool {
	return len(d.ops) == 0 || d.ops[extmtp.OC_MTP_SetObjectPropValue]
}
