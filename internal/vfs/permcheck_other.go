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

// Proactive permission prediction is Linux-only, same as elevation itself
// (see elevate_other.go) — these always report "no elevation needed"
// (never eagerly true), so the plain attempt always runs first, same as
// shfm's behaviour before permcheck_linux.go existed.
func chmodNeedsElevation(path string) bool                     { return false }
func chownNeedsElevation(path string, newUID, newGID int) bool { return false }
func removeOrRenameNeedsElevation(path string) bool            { return false }
func renameNeedsElevation(oldPath, newPath string) bool        { return false }
