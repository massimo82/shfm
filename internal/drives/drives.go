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

// Package drives finds the available local drives/mounts and keeps the
// list of network connections (SMB/NFS/SFTP) configured by the user.
package drives

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// LocalDrive represents a local mount point.
type LocalDrive struct {
	MountPoint string
	FSType     string
	Device     string
	Removable  bool
	Total      uint64
	Free       uint64
	// Vendor/Model identify the underlying disk hardware (read from sysfs
	// on Linux, e.g. "Samsung SSD 860 EVO 1TB"), for a human-recognizable
	// label in the source picker; empty when undeterminable (other
	// platforms, virtual/network block devices, permissions).
	Vendor string
	Model  string
}

// pseudoFS lists the virtual/system filesystems to hide from the list of
// selectable drives.
var pseudoFS = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
	"cgroup": true, "cgroup2": true, "pstore": true, "bpf": true, "tracefs": true,
	"debugfs": true, "securityfs": true, "mqueue": true, "hugetlbfs": true,
	"autofs": true, "rpc_pipefs": true, "binfmt_misc": true, "configfs": true,
	"fusectl": true, "overlay": false,
}

// ListLocal reads /proc/mounts and lists the "real" mounts (disks,
// partitions, removable devices), excluding virtual system filesystems.
func ListLocal() ([]LocalDrive, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []LocalDrive
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		device, mountPoint, fstype := fields[0], unescapeMount(fields[1]), fields[2]
		if pseudoFS[fstype] {
			continue
		}
		if !strings.HasPrefix(device, "/dev/") && fstype != "nfs" && fstype != "nfs4" &&
			fstype != "cifs" && device != "/" {
			continue
		}
		removable := strings.HasPrefix(mountPoint, "/media/") || strings.HasPrefix(mountPoint, "/run/media/")
		total, free := diskUsage(mountPoint)
		vendor, model := diskInfo(diskBaseName(device))
		out = append(out, LocalDrive{
			MountPoint: mountPoint,
			FSType:     fstype,
			Device:     device,
			Removable:  removable,
			Total:      total,
			Free:       free,
			Vendor:     vendor,
			Model:      model,
		})
	}
	return out, nil
}

// unescapeMount decodes the octal escape sequences /proc/mounts uses for
// spaces and special characters in paths (e.g. \040 for a space).
func unescapeMount(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseInt(s[i+1:i+4], 8, 32); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
