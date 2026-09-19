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
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// diskWithPartition matches an nvme/mmcblk PARTITION's /sys/block-style
// name ("nvme0n1p1", "mmcblk0p1"), capturing the parent disk's own name
// ("nvme0n1", "mmcblk0") — these need special handling because, unlike
// "sda"/"sda1", the disk name itself already ends in a digit, so a plain
// trailing-digit trim would cut into it instead of just the partition
// number.
var (
	diskWithPartition = regexp.MustCompile(`^((?:nvme\d+n\d+|mmcblk\d+))p\d+$`)
	nvmeOrMMCDiskOnly = regexp.MustCompile(`^(?:nvme\d+n\d+|mmcblk\d+)$`)
)

// diskBaseName resolves a device path (as found in /proc/mounts, e.g.
// "/dev/sda1" or "/dev/nvme0n1p1") to its parent disk's name under
// /sys/block ("sda", "nvme0n1"), which is where the vendor/model files
// diskInfo reads actually live — a partition itself has no "device" link.
func diskBaseName(device string) string {
	name := strings.TrimPrefix(device, "/dev/")
	if m := diskWithPartition.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if nvmeOrMMCDiskOnly.MatchString(name) {
		// Already a whole-disk name (e.g. "nvme0n1" mounted directly, no
		// partition table): its trailing digit is part of the disk name
		// itself, not a partition number to strip.
		return name
	}
	return strings.TrimRight(name, "0123456789")
}

// diskInfo reads the vendor/model strings sysfs exposes for a disk (by its
// /sys/block name, e.g. "sda" — see diskBaseName), used to show a
// human-recognizable name ("Samsung SSD 860 EVO") in the source picker
// instead of just a device path. "ATA" is filtered out of vendor: it's what
// the SCSI-ATA translation layer reports for every SATA-connected disk
// regardless of actual manufacturer, which is what the model string itself
// already names (e.g. model "Samsung SSD 860 EVO 1TB").
func diskInfo(disk string) (vendor, model string) {
	vendor = readSysfsTrim(filepath.Join("/sys/block", disk, "device", "vendor"))
	if vendor == "ATA" || vendor == "SCSI" {
		vendor = ""
	}
	model = readSysfsTrim(filepath.Join("/sys/block", disk, "device", "model"))
	return vendor, model
}

func readSysfsTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(data)), " ")
}
