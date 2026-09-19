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
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/godbus/dbus/v5"
)

// FSType identifies a filesystem shfm can format a device with.
type FSType string

const (
	FSExFAT FSType = "exfat"
	FSFAT32 FSType = "vfat" // udisks2/mkfs naming; produces FAT32 for partitions above the small FAT16 threshold
	FSExt4  FSType = "ext4"
	FSXFS   FSType = "xfs"
)

// FormatChoices lists the filesystems offered in the "format a source"
// dialog, together with a human-friendly label.
var FormatChoices = []struct {
	Type  FSType
	Label string
}{
	{FSExFAT, "exFAT"},
	{FSFAT32, "FAT32"},
	{FSExt4, "ext4"},
	{FSXFS, "XFS"},
}

var (
	reMMCPart  = regexp.MustCompile(`^(mmcblk\d+)p\d+$`)
	reNVMePart = regexp.MustCompile(`^(nvme\d+n\d+)p\d+$`)
	reSDPart   = regexp.MustCompile(`^([a-z]+)\d+$`)
)

// diskNameFromPartition strips a partition suffix (e.g. "sdb1" -> "sdb",
// "mmcblk0p1" -> "mmcblk0", "nvme0n1p1" -> "nvme0n1"); if base doesn't look
// like a partition, it's returned unchanged (it's already a whole-disk name).
func diskNameFromPartition(base string) string {
	if m := reMMCPart.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	if m := reNVMePart.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	if m := reSDPart.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	return base
}

// SystemDiskName returns the base disk name (e.g. "vda", "sda") backing the
// root filesystem ("/"), used to refuse formatting the disk shfm itself (or
// the OS) is running from — a hard safety rail independent of, and in
// addition to, the UI's own confirmation dialogs.
func SystemDiskName() (string, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || fields[1] != "/" {
			continue
		}
		dev := fields[0]
		if !strings.HasPrefix(dev, "/dev/") {
			return "", fmt.Errorf("root filesystem is not on a local block device (%s)", dev)
		}
		return diskNameFromPartition(strings.TrimPrefix(dev, "/dev/")), nil
	}
	return "", fmt.Errorf("could not find the root filesystem in /proc/mounts")
}

// IsSystemDisk reports whether devicePath (e.g. "/dev/sdb" or "/dev/sdb1")
// resides on the same physical disk as the running system's root
// filesystem.
func IsSystemDisk(devicePath string) bool {
	sysDisk, err := SystemDiskName()
	if err != nil {
		// If we can't determine the system disk, err on the side of
		// caution and treat the target as if it were the system disk, so
		// formatting is refused rather than silently allowed.
		return true
	}
	name := diskNameFromPartition(strings.TrimPrefix(devicePath, "/dev/"))
	return name == sysDisk
}

// WholeDiskDevicePath strips any partition suffix from devicePath (e.g.
// "/dev/sdb1" -> "/dev/sdb"), for callers (like the format flow) that need
// to operate on the whole disk rather than one of its partitions.
func WholeDiskDevicePath(devicePath string) string {
	return "/dev/" + diskNameFromPartition(strings.TrimPrefix(devicePath, "/dev/"))
}

// FormatDevice repartitions wholeDiskPath (e.g. "/dev/sdb" — the *whole*
// disk, not one of its partitions) with a fresh partition table containing
// a single primary partition spanning the entire disk, formatted with
// fsType. This PERMANENTLY DESTROYS all data currently on the device.
//
// Implemented entirely over the udisks2 D-Bus API (org.freedesktop.UDisks2
// Block.Format / PartitionTable.CreatePartition) — the same mechanism
// GNOME Disks uses — so, like mounting, it works for the logged-in local
// user without root, via udisks2's default polkit rules, and without
// shelling out to mkfs.* or parted/sfdisk directly.
func FormatDevice(wholeDiskPath string, fsType FSType) error {
	if IsSystemDisk(wholeDiskPath) {
		return fmt.Errorf("refusing to format %s: it holds the system disk", wholeDiskPath)
	}

	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return fmt.Errorf("cannot reach the system D-Bus (is udisks2 running?): %w", err)
	}
	defer conn.Close()

	diskObj := conn.Object(udisksService, blockObjectPath(wholeDiskPath))

	// 1. Fresh partition table (MBR/"dos", for maximum compatibility with
	// the kind of small removable media this feature targets).
	call := diskObj.Call(udisksService+".Block.Format", 0, "dos", map[string]dbus.Variant{})
	if call.Err != nil {
		return fmt.Errorf("creating partition table on %s: %w", wholeDiskPath, call.Err)
	}

	// 2. A single primary partition spanning the whole disk (offset=0,
	// size=0 tells udisks2 to use all available space).
	var partPath dbus.ObjectPath
	call = diskObj.Call(udisksService+".PartitionTable.CreatePartition", 0,
		uint64(0), uint64(0), "", "", map[string]dbus.Variant{})
	if call.Err != nil {
		return fmt.Errorf("creating partition on %s: %w", wholeDiskPath, call.Err)
	}
	if err := call.Store(&partPath); err != nil {
		return fmt.Errorf("unexpected reply while creating the partition: %w", err)
	}

	// 3. Format that partition with the requested filesystem.
	partObj := conn.Object(udisksService, partPath)
	call = partObj.Call(udisksService+".Block.Format", 0, string(fsType), map[string]dbus.Variant{})
	if call.Err != nil {
		return fmt.Errorf("formatting the new partition as %s: %w", fsType, call.Err)
	}
	return nil
}
