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

type FSType string

const (
	FSExFAT FSType = "exfat"
	FSFAT32 FSType = "vfat"
	FSExt4  FSType = "ext4"
	FSXFS   FSType = "xfs"
)

var FormatChoices = []struct {
	Type  FSType
	Label string
}{
	{FSExFAT, "exFAT"},
	{FSFAT32, "FAT32"},
	{FSExt4, "ext4"},
	{FSXFS, "XFS"},
}

func SystemDiskName() (string, error)              { return "", fmt.Errorf("not supported on this platform") }
func IsSystemDisk(devicePath string) bool          { return true }
func WholeDiskDevicePath(devicePath string) string { return devicePath }
func FormatDevice(wholeDiskPath string, fsType FSType) error {
	return fmt.Errorf("formatting devices is not supported on this platform")
}
