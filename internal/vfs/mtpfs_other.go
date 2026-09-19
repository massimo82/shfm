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

//go:build !linux

package vfs

import (
	"fmt"
	"io"

	"shfm/internal/mtp"
)

// MTPFS is not supported on this platform (the real backend, based on
// Linux usbfs ioctls, is in mtpfs.go). It still fully implements the
// FileSystem interface — always returning an error — only to satisfy the
// compiler's type-checking on other platforms; it is never successfully
// instantiated, since DialMTP always fails here.
type MTPFS struct{}

var errMTPUnsupported = fmt.Errorf("MTP support is not available on this platform")

// DialMTP is not supported on this platform.
func DialMTP(info mtp.DeviceInfo) (*MTPFS, error) { return nil, errMTPUnsupported }

func (m *MTPFS) Kind() Kind                                 { return KindMTP }
func (m *MTPFS) Label() string                              { return "" }
func (m *MTPFS) Root() string                               { return "/" }
func (m *MTPFS) List(path string) ([]Entry, error)          { return nil, errMTPUnsupported }
func (m *MTPFS) Stat(path string) (Entry, error)            { return Entry{}, errMTPUnsupported }
func (m *MTPFS) Mkdir(path string) error                    { return errMTPUnsupported }
func (m *MTPFS) CreateEmptyFile(path string) error          { return errMTPUnsupported }
func (m *MTPFS) Remove(path string) error                   { return errMTPUnsupported }
func (m *MTPFS) Rename(oldPath, newPath string) error       { return errMTPUnsupported }
func (m *MTPFS) Open(path string) (io.ReadCloser, error)    { return nil, errMTPUnsupported }
func (m *MTPFS) Create(path string) (io.WriteCloser, error) { return nil, errMTPUnsupported }
func (m *MTPFS) Join(elem ...string) string                 { return "/" }
func (m *MTPFS) Dir(path string) string                     { return "/" }
func (m *MTPFS) Base(path string) string                    { return "" }
func (m *MTPFS) SupportsTrash() bool                        { return false }
func (m *MTPFS) Close() error                               { return nil }
