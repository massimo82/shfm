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

package drives

import "fmt"

// RemovableDevice represents a removable device (Linux only for now).
type RemovableDevice struct {
	Name       string
	Path       string
	SizeBytes  uint64
	Mounted    bool
	MountPoint string
	FSType     string
	Vendor     string
	Model      string
}

// ListRemovable is not supported on this platform: detection is
// implemented via sysfs (/sys/block), available only on Linux.
func ListRemovable() ([]RemovableDevice, error) { return nil, nil }

// TryAutoMount is not supported on this platform.
func TryAutoMount(devicePath, name string) (string, error) {
	return "", fmt.Errorf("automatic mounting of removable devices is not supported on this platform")
}

// Unmount is not supported on this platform.
func Unmount(devicePath, mountPoint string) error {
	return fmt.Errorf("unmounting is not supported on this platform")
}
