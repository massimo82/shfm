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

// Package mtp exposes MTP (Media Transfer Protocol) devices — Android
// smartphones, cameras, media players — connected over USB, through a
// small API tailored to shfm's needs (device discovery + a handle-based
// file/folder API). The actual PTP/MTP protocol and USB transport are
// delegated to github.com/hanwen/go-mtpfs/mtp, a mature, widely used
// implementation (it backs go-mtpfs's own FUSE mount of MTP devices),
// rather than reimplemented here: getting the wire-level details exactly
// right against the many real-world MTP responder quirks is exactly the
// kind of thing worth not reinventing.
package mtp

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	extmtp "github.com/hanwen/go-mtpfs/mtp"
	"github.com/hanwen/usb"
)

// RootHandle is the conventional handle used as the "parent" of objects at
// the root of a storage: it is not a real PTP object handle.
const RootHandle uint32 = 0x00000000

// FormatAssociation is the PTP object format code for a folder.
const FormatAssociation uint16 = extmtp.OFC_Association

// ErrNotSupported indicates the device replied "operation not supported" to
// an optional operation (typically rename): the caller can fall back to
// copy+delete.
var ErrNotSupported = errors.New("mtp: operation not supported by the device")

// DeviceInfo identifies an MTP device found on the USB bus, before it's
// opened: enough to populate an entry in the source picker menu and to
// reselect the same physical device later, via Open.
type DeviceInfo struct {
	id string // manufacturer + product + serial, per (*extmtp.Device).ID()
}

// Label returns a human-readable description of the device for the UI.
func (d DeviceInfo) Label() string {
	if d.id == "" {
		return "MTP device"
	}
	return d.id
}

// DiscoverDevices lists MTP-looking devices on the USB bus. Each candidate
// is briefly opened (USB-level only, no PTP session) just long enough to
// read its identifying string descriptors for the label — real MTP
// responders don't expose those without an open USB handle.
func DiscoverDevices() ([]DeviceInfo, error) {
	ctx := usb.NewContext()
	defer ctx.Exit()

	devs, err := extmtp.FindDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DeviceInfo, 0, len(devs))
	for _, d := range devs {
		if err := d.Open(); err != nil {
			continue
		}
		if err := d.Configure(); err != nil {
			d.Close()
			continue
		}
		id, err := d.ID()
		d.Close()
		if err != nil || id == "" {
			continue
		}
		out = append(out, DeviceInfo{id: id})
	}
	return out, nil
}

// Device represents an open MTP session towards a device.
type Device struct {
	dev       *extmtp.Device
	storageID uint32
}

// Open selects and opens the device described by info (as previously found
// by DiscoverDevices), starts a PTP session and picks the first available
// storage as the default one.
func Open(info DeviceInfo) (*Device, error) {
	dev, err := extmtp.SelectDevice(info.id)
	if err != nil {
		return nil, err
	}
	if err := dev.OpenSession(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("opening MTP session failed: %w", err)
	}
	var storageIDs extmtp.Uint32Array
	if err := dev.GetStorageIDs(&storageIDs); err != nil {
		dev.CloseSession()
		dev.Close()
		return nil, err
	}
	if len(storageIDs.Values) == 0 {
		dev.CloseSession()
		dev.Close()
		return nil, fmt.Errorf("mtp: no storage available on the device (SD card not mounted / device locked?)")
	}
	return &Device{dev: dev, storageID: storageIDs.Values[0]}, nil
}

func (d *Device) Close() error {
	_ = d.dev.CloseSession()
	return d.dev.Close()
}

// ObjectInfo is the subset of the PTP ObjectInfo dataset fields shfm uses.
type ObjectInfo struct {
	Filename         string
	ObjectFormat     uint16
	ObjectSize       uint32
	ModificationDate time.Time
}

// GetObjectHandles lists the handles of the direct child objects of parent
// (RootHandle for the default storage's root).
func (d *Device) GetObjectHandles(parent uint32) ([]uint32, error) {
	// On the wire, GetObjectHandles' AssociationHandle parameter doesn't use
	// 0x00000000 to mean "the root": it means "no filtering by parent at
	// all", i.e. every object on the entire storage, flattened. Root-level
	// objects only (what our RootHandle=0 means to callers) are requested
	// with the dedicated sentinel 0xFFFFFFFF instead.
	assoc := parent
	if assoc == RootHandle {
		assoc = 0xFFFFFFFF
	}
	var arr extmtp.Uint32Array
	if err := d.dev.GetObjectHandles(d.storageID, 0, assoc, &arr); err != nil {
		return nil, err
	}
	return arr.Values, nil
}

// GetObjectInfo retrieves the metadata (name, format, size, mtime...) of an
// object given its handle.
func (d *Device) GetObjectInfo(handle uint32) (ObjectInfo, error) {
	var oi extmtp.ObjectInfo
	if err := d.dev.GetObjectInfo(handle, &oi); err != nil {
		return ObjectInfo{}, err
	}
	return ObjectInfo{
		Filename:         oi.Filename,
		ObjectFormat:     oi.ObjectFormat,
		ObjectSize:       oi.CompressedSize,
		ModificationDate: oi.ModificationDate,
	}, nil
}

// DeleteObject deletes (recursively, if it's a folder, as most MTP
// responders interpret the operation) the given object.
func (d *Device) DeleteObject(handle uint32) error {
	return d.dev.DeleteObject(handle)
}

// Mkdir creates a new folder (an "Association" object) as a child of parent
// and returns its newly assigned handle.
func (d *Device) Mkdir(parent uint32, name string) (uint32, error) {
	oi := extmtp.ObjectInfo{
		ObjectFormat:    extmtp.OFC_Association,
		AssociationType: extmtp.AT_GenericFolder,
		ParentObject:    parent,
		Filename:        name,
	}
	_, _, handle, err := d.dev.SendObjectInfo(d.storageID, parent, &oi)
	return handle, err
}

// Rename attempts to rename an object via the MTP SetObjectPropValue
// operation on the ObjectFilename property. Not every device supports it:
// in that case it returns ErrNotSupported, and the caller can fall back to
// copy+delete.
func (d *Device) Rename(handle uint32, newName string) error {
	err := d.dev.SetObjectPropValue(handle, extmtp.OPC_ObjectFileName, extmtp.StringValue{Value: newName})
	if err == nil {
		return nil
	}
	if rc, ok := err.(extmtp.RCError); ok && rc == extmtp.RC_OperationNotSupported {
		return ErrNotSupported
	}
	return err
}

// GetObjectReader starts streaming the download of an object: returns an
// io.ReadCloser fed, in the background, from the underlying library's
// whole-object GetObject call via an in-memory pipe.
func (d *Device) GetObjectReader(handle uint32) (io.ReadCloser, error) {
	pr, pw := io.Pipe()
	go func() {
		err := d.dev.GetObject(handle, pw)
		pw.CloseWithError(err)
	}()
	return pr, nil
}

// NewObjectWriter starts streaming the upload of a new file of known size
// (required by the MTP protocol, which declares it in the ObjectInfo
// dataset before starting the data phase) as a child of parent.
func (d *Device) NewObjectWriter(parent uint32, name string, size int64) (io.WriteCloser, error) {
	oi := extmtp.ObjectInfo{
		ObjectFormat:   guessObjectFormat(name),
		ParentObject:   parent,
		Filename:       name,
		CompressedSize: uint32(size),
	}
	if _, _, _, err := d.dev.SendObjectInfo(d.storageID, parent, &oi); err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := d.dev.SendObject(pr, size)
		pr.CloseWithError(err)
		done <- err
	}()
	return &pipeUpload{pw: pw, done: done}, nil
}

// pipeUpload adapts the library's whole-object SendObject(io.Reader, size)
// call, running in its own goroutine, to the streaming io.WriteCloser shfm's
// copy machinery expects: Close blocks until that goroutine has fully
// finished (success or error), so the caller can safely release the MTP
// session afterwards.
type pipeUpload struct {
	pw   *io.PipeWriter
	done chan error
}

func (u *pipeUpload) Write(p []byte) (int, error) { return u.pw.Write(p) }

func (u *pipeUpload) Close() error {
	u.pw.Close()
	return <-u.done
}

// guessObjectFormat picks a reasonable PTP format code based on the file's
// extension. Not essential for correctness (many devices happily accept
// OFC_Undefined), but some behave better when the declared format matches
// the content.
func guessObjectFormat(name string) uint16 {
	ext := strings.ToLower(name)
	switch {
	case strings.HasSuffix(ext, ".jpg"), strings.HasSuffix(ext, ".jpeg"):
		return extmtp.OFC_EXIF_JPEG
	case strings.HasSuffix(ext, ".png"):
		return extmtp.OFC_PNG
	case strings.HasSuffix(ext, ".mp3"):
		return extmtp.OFC_MP3
	case strings.HasSuffix(ext, ".mp4"), strings.HasSuffix(ext, ".m4v"):
		return extmtp.OFC_MTP_MP4
	case strings.HasSuffix(ext, ".txt"):
		return extmtp.OFC_Text
	default:
		return extmtp.OFC_Undefined
	}
}
