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

// Package fusemount exposes network sources as local folders through FUSE;
// only implemented on Linux (manager.go). Here every source is reported as
// not mountable, so remote files are opened via a downloaded temp copy.
package fusemount

import (
	"os"
	"path/filepath"

	"shfm/internal/vfs"
)

type Manager struct{}

func NewManager(base string) *Manager { return &Manager{} }

func DefaultBase() string { return filepath.Join(os.TempDir(), "shfm") }

func Supported(src vfs.FileSystem) bool { return false }

func (mg *Manager) LocalPath(src vfs.FileSystem, vfsPath string) (string, error) {
	return "", vfs.ErrNotSupported
}

func (mg *Manager) Busy() bool { return false }

func (mg *Manager) Release(src vfs.FileSystem) bool { return false }

func (mg *Manager) Session(kind vfs.Kind, label string) vfs.FileSystem { return nil }

func (mg *Manager) UnmountAll() {}
