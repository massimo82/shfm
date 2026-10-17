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

package drives

import (
	"fmt"

	"github.com/godbus/dbus/v5"

	"shfm/internal/applog"
)

// Eject makes wholeDiskPath (e.g. "/dev/sdb") safe to pull out: every
// filesystem on it is unmounted, which writes out whatever is still
// cached for it, then the drive is powered off (or its medium ejected,
// for a card reader) through udisks2, as desktop file managers' "Safely
// remove" does. poweredOff tells whether that last step happened: when
// it can't (no udisks2, a drive that can't be powered off), the disk is
// unmounted all the same, which is what makes pulling it out safe.
func Eject(wholeDiskPath string) (poweredOff bool, err error) {
	if IsSystemDisk(wholeDiskPath) {
		return false, fmt.Errorf("%s holds the system", wholeDiskPath)
	}
	if err := unmountDisk(wholeDiskPath); err != nil {
		return false, err
	}
	if err := powerOffViaUDisks2(wholeDiskPath); err != nil {
		applog.Warn("drive not powered off", "disk", wholeDiskPath, "error", err)
		return false, nil
	}
	return true, nil
}

// powerOffViaUDisks2 powers off the drive holding the block device
// wholeDiskPath, or ejects its medium when the drive can't be powered off
// but the medium can be taken out.
func powerOffViaUDisks2(wholeDiskPath string) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("cannot reach the system D-Bus: %w", err)
	}
	defer conn.Close()

	prop, err := conn.Object(udisksService, blockObjectPath(wholeDiskPath)).
		GetProperty(udisksService + ".Block.Drive")
	if err != nil {
		return udisksError(err)
	}
	drivePath, ok := prop.Value().(dbus.ObjectPath)
	if !ok || drivePath == "/" {
		return fmt.Errorf("no drive for %s", wholeDiskPath)
	}
	drive := conn.Object(udisksService, drivePath)
	method := ""
	if v, err := drive.GetProperty(udisksService + ".Drive.CanPowerOff"); err == nil && v.Value() == true {
		method = "PowerOff"
	} else if v, err := drive.GetProperty(udisksService + ".Drive.Ejectable"); err == nil && v.Value() == true {
		method = "Eject"
	} else {
		return fmt.Errorf("%s can't be powered off", wholeDiskPath)
	}
	if call := drive.Call(udisksService+".Drive."+method, 0, map[string]dbus.Variant{}); call.Err != nil {
		return udisksError(call.Err)
	}
	return nil
}
