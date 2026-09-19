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
	"strings"

	"github.com/godbus/dbus/v5"
)

// This file talks to the system's udisks2 daemon over D-Bus — the same
// mechanism graphical file managers (Nautilus, Thunar, GVfs...) use to
// mount/unmount removable media. It matters because calling the mount(2)
// syscall directly (see tryDirectMount in removable_linux.go) almost always
// requires root, while udisks2's default polkit rules let the user who is
// logged into the active local session mount/unmount removable media
// without a password prompt — exactly why tools like Thunar "just work" for
// a regular user. Implemented with github.com/godbus/dbus/v5, a pure-Go
// D-Bus client library (no cgo, no external dbus-send/udisksctl commands).

const (
	udisksService         = "org.freedesktop.UDisks2"
	udisksFilesystemIface = udisksService + ".Filesystem"
)

// blockObjectPath returns the UDisks2 D-Bus object path for a device like
// "/dev/sdb1", following udisks2's deterministic naming convention (the
// object path is simply the device's base name under a fixed prefix).
func blockObjectPath(devicePath string) dbus.ObjectPath {
	name := strings.TrimPrefix(devicePath, "/dev/")
	return dbus.ObjectPath("/org/freedesktop/UDisks2/block_devices/" + name)
}

// MountViaUDisks2 asks udisks2 to mount devicePath and returns the resulting
// mount point.
func MountViaUDisks2(devicePath string) (string, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return "", fmt.Errorf("cannot reach the system D-Bus (is udisks2 running?): %w", err)
	}
	defer conn.Close()

	obj := conn.Object(udisksService, blockObjectPath(devicePath))
	var mountPath string
	call := obj.Call(udisksFilesystemIface+".Mount", 0, map[string]dbus.Variant{})
	if call.Err != nil {
		return "", fmt.Errorf("udisks2: %w", call.Err)
	}
	if err := call.Store(&mountPath); err != nil {
		return "", fmt.Errorf("udisks2: unexpected reply: %w", err)
	}
	return mountPath, nil
}

// UnmountViaUDisks2 asks udisks2 to unmount the filesystem on devicePath.
func UnmountViaUDisks2(devicePath string) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("cannot reach the system D-Bus (is udisks2 running?): %w", err)
	}
	defer conn.Close()

	obj := conn.Object(udisksService, blockObjectPath(devicePath))
	call := obj.Call(udisksFilesystemIface+".Unmount", 0, map[string]dbus.Variant{})
	if call.Err != nil {
		return fmt.Errorf("udisks2: %w", call.Err)
	}
	return nil
}
