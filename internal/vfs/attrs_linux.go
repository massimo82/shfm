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
	"os"

	"golang.org/x/sys/unix"
)

// linux/fs.h inode flags, as returned by FS_IOC_GETFLAGS.
const (
	fsImmutableFL = 0x00000010
	fsAppendFL    = 0x00000020
)

// readAttrsPlatform reads path's flags with the FS_IOC_GETFLAGS ioctl, the
// same call lsattr(1) makes. Only regular files and directories are asked
// (opening a FIFO or device just to query it could block or have side
// effects, and a symlink has no flags of its own), and any failure — a
// filesystem without the ioctl (ENOTTY/EOPNOTSUPP), no read permission to
// open the entry — simply means "unknown".
func readAttrsPlatform(path string) (attrSet, bool) {
	info, err := os.Lstat(path)
	if err != nil || !(info.Mode().IsRegular() || info.IsDir()) {
		return 0, false
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, false
	}
	defer unix.Close(fd)
	flags, err := unix.IoctlGetInt(fd, unix.FS_IOC_GETFLAGS)
	if err != nil {
		return 0, false
	}
	var a attrSet
	if flags&fsImmutableFL != 0 {
		a |= attrImmutable
	}
	if flags&fsAppendFL != 0 {
		a |= attrAppendOnly
	}
	return a, true
}
