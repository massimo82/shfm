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

//go:build linux || darwin

package trash

import (
	"bufio"
	"os"
	"strings"
	"syscall"
)

// sameDeviceInfo compares the device id (st_dev) of two os.FileInfo.
func sameDeviceInfo(a, b os.FileInfo) bool {
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		return false
	}
	return sa.Dev == sb.Dev
}

// findMountPoint locates the mount point of the filesystem containing path,
// by reading /proc/mounts (Linux). On systems where /proc/mounts isn't
// available it returns "" and the caller falls back to the home trash.
func findMountPoint(path string) string {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return ""
	}
	defer f.Close()

	best := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		mp := fields[1]
		if mp == "/" {
			if best == "" {
				best = mp
			}
			continue
		}
		if strings.HasPrefix(path, mp+"/") || path == mp {
			if len(mp) > len(best) {
				best = mp
			}
		}
	}
	if best == "" {
		return "/"
	}
	return best
}
