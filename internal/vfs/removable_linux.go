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

package vfs

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Writes to a removable drive (a USB stick, an SD card, a disk in a USB
// enclosure) sit in the page cache until the kernel gets round to them:
// a copy "finishes" in seconds and the drive, pulled out right after,
// holds truncated files. So on such a drive what shfm writes is flushed
// as it goes (see flushFile) and once an operation is over (FlushRemovable).

// sysRoot and mountInfoPath are where the kernel's view of block devices
// and mounts is read from; tests point them at a fake tree.
var (
	sysRoot       = "/sys"
	mountInfoPath = "/proc/self/mountinfo"
)

var (
	removableMu    sync.Mutex
	removableCache = map[uint64]bool{} // by st_dev
)

// onRemovable reports whether path (or, if it doesn't exist yet, the
// nearest existing parent) is on a removable drive, by the same rule as
// the drives package: the kernel's "removable" flag, or a disk attached
// through USB.
var onRemovable = func(path string) bool {
	var st unix.Stat_t
	for {
		if unix.Stat(path, &st) == nil {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
	removableMu.Lock()
	defer removableMu.Unlock()
	if r, ok := removableCache[st.Dev]; ok {
		return r
	}
	r := devRemovable(st.Dev)
	removableCache[st.Dev] = r
	return r
}

// devRemovable resolves dev (a mounted filesystem's st_dev) to its block
// device in sysfs. A FUSE filesystem (ntfs-3g, exfat-fuse) has an
// anonymous device: the block device is then the mount's source.
func devRemovable(dev uint64) bool {
	if unix.Major(dev) == 0 {
		src := mountSource(dev)
		if !strings.HasPrefix(src, "/dev/") {
			return false
		}
		var st unix.Stat_t
		if unix.Stat(src, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFBLK {
			return false
		}
		dev = st.Rdev
	}
	p, err := filepath.EvalSymlinks(filepath.Join(sysRoot, "dev", "block",
		fmt.Sprintf("%d:%d", unix.Major(dev), unix.Minor(dev))))
	if err != nil {
		return false
	}
	// p is .../block/sdb/sdb1 for a partition, .../block/sdb for a disk;
	// device-mapper or loop devices on top of a stick aren't followed.
	disk := p
	if _, err := os.Stat(filepath.Join(p, "partition")); err == nil {
		disk = filepath.Dir(p)
	}
	if strings.Contains(disk, "/usb") {
		return true
	}
	flag, err := os.ReadFile(filepath.Join(disk, "removable"))
	return err == nil && strings.TrimSpace(string(flag)) == "1"
}

// mountSource is the source (e.g. /dev/sdb1) of the mount whose device is
// dev, from mountinfo: "id parent maj:min root mountpoint opts ... - type
// source superopts".
func mountSource(dev uint64) string {
	f, err := os.Open(mountInfoPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	want := fmt.Sprintf("%d:%d", unix.Major(dev), unix.Minor(dev))
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 || fields[2] != want {
			continue
		}
		for i, fl := range fields {
			if fl == "-" && i+2 < len(fields) {
				return unescapeMountField(fields[i+2])
			}
		}
	}
	return ""
}

// FlushRemovable makes sure everything written so far to the filesystem
// holding path (a local path) has reached the drive, when that's a
// removable one: call it once an operation writing there is over.
// Elsewhere it does nothing.
func FlushRemovable(path string) error {
	if !onRemovable(path) {
		return nil
	}
	for {
		f, err := os.Open(path)
		if err == nil {
			defer f.Close()
			if err := unix.Syncfs(int(f.Fd())); err != nil {
				return fmt.Errorf("flushing the drive: %w", err)
			}
			return nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}
