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
	"time"

	"golang.org/x/sys/unix"
)

// birthTime returns path's creation ("birth") time via the statx(2)
// syscall (available since Linux 4.11), which — unlike the classic
// stat(2)/lstat(2) — can report it on filesystems that record it (most
// modern ones: ext4, xfs, btrfs...). Not every filesystem does (tmpfs,
// some network filesystems, older ext4 without the right feature flags),
// in which case the STATX_BTIME bit in the returned mask is unset and this
// returns the zero time — callers should treat that as "unknown", not
// "epoch", and simply omit it rather than showing a misleading date.
func birthTime(path string, followSymlink bool) time.Time {
	var flags int
	if !followSymlink {
		flags |= unix.AT_SYMLINK_NOFOLLOW
	}
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, flags, unix.STATX_BTIME, &stx); err != nil {
		return time.Time{}
	}
	if stx.Mask&unix.STATX_BTIME == 0 {
		return time.Time{} // this filesystem doesn't record a birth time
	}
	return time.Unix(stx.Btime.Sec, int64(stx.Btime.Nsec))
}
