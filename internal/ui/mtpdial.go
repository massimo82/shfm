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

package ui

import (
	"shfm/internal/mtp"
	"shfm/internal/vfs"
)

// mtpDialer returns the function connecting to an MTP device. A device
// accepts a single session: if a FUSE mount still holds one with it (an app
// opened a file from it, and the pane has since moved elsewhere), that
// session is reused rather than failing to open a second one.
func (m *Model) mtpDialer(dev mtp.DeviceInfo) func() (vfs.FileSystem, error) {
	mounts := m.mounts
	return func() (vfs.FileSystem, error) {
		if mounts != nil {
			if fs := mounts.Session(vfs.KindMTP, vfs.MTPLabel(dev)); fs != nil {
				return fs, nil
			}
		}
		return vfs.DialMTP(dev)
	}
}
