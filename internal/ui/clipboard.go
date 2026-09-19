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

package ui

import "shfm/internal/vfs"

// Clipboard holds the items copied (with Ctrl+C) awaiting a paste, along
// with their source filesystem and folder. Whether a paste copies (Ctrl+V)
// or moves (Ctrl+Alt+V) is decided at paste time, not at copy time, so
// there's no separate "cut" state to track.
type Clipboard struct {
	FS    vfs.FileSystem
	Dir   string
	Names []string
}

func (c Clipboard) Empty() bool { return c.FS == nil || len(c.Names) == 0 }

func (c Clipboard) Paths() []string {
	paths := make([]string, len(c.Names))
	for i, n := range c.Names {
		paths[i] = c.FS.Join(c.Dir, n)
	}
	return paths
}
