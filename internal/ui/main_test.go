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
	"os"
	"testing"

	"shfm/internal/drives"
	"shfm/internal/mtp"
)

// TestMain keeps the tests off the machine's hardware: the source picker
// lists no disks and no USB devices (a CI runner may have no usbfs at all,
// and the tests must not depend on what's plugged in).
func TestMain(m *testing.M) {
	listLocalDrives = func() ([]drives.LocalDrive, error) { return nil, nil }
	listRemovableDrives = func() ([]drives.RemovableDevice, error) { return nil, nil }
	discoverMTPDevices = func() ([]mtp.DeviceInfo, error) { return nil, nil }
	os.Exit(m.Run())
}
