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

package vfs

import "time"

// birthTime is not supported on this platform: Go's standard library has
// no portable way to query a file's creation time (only Linux's statx(2)
// with STATX_BTIME is used here; other platforms have their own
// mechanisms — e.g. macOS's stat's st_birthtimespec, Windows'
// CreationTime — deliberately not implemented to avoid growing platform-
// specific dependencies for a project focused on Linux). Returns the zero
// time, which callers treat as "unknown" and simply omit.
func birthTime(path string, followSymlink bool) time.Time { return time.Time{} }
