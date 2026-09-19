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

package vfs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// This file deals with the inode attribute flags set with chattr(1) and shown
// by lsattr(1) that make an entry unchangeable *even by root*: "immutable"
// (+i: no writes, no chmod/chown, no delete/rename/link) and "append-only"
// (+a: like immutable, except data can still be appended). pkexec can't fix
// those — root is refused just the same — so, unlike an ordinary permission
// error, shfm must not ask for a password and then fail anyway: it detects
// the flag up front and says what's wrong instead.

// attrSet is the set of restricting attribute flags found on an entry.
type attrSet uint8

const (
	attrImmutable attrSet = 1 << iota
	attrAppendOnly
)

// names returns the human-readable names of the flags in a.
func (a attrSet) names() []string {
	var out []string
	if a&attrImmutable != 0 {
		out = append(out, "immutable")
	}
	if a&attrAppendOnly != 0 {
		out = append(out, "append-only")
	}
	return out
}

// readAttrs reports path's attribute flags, or ok=false when they can't be
// determined (unsupported platform or filesystem, a symlink or special file,
// no permission to open it): callers then behave as if there were none, as
// before this file existed. A variable so tests can substitute it (setting
// real flags needs root).
var readAttrs = readAttrsPlatform

// AttrError is returned when an operation is refused up front because the
// entry, or the directory that would have to change, carries an attribute
// flag that not even root can override.
type AttrError struct {
	Path string
	Attr string // "immutable" or "append-only"
}

func (e *AttrError) Error() string {
	flag := "i"
	if e.Attr == "append-only" {
		flag = "a"
	}
	return fmt.Sprintf("%s is %s (chattr +%s): it can't be changed or removed, not even as root; clear the flag first with: chattr -%s -- %s",
		e.Path, e.Attr, flag, flag, e.Path)
}

// blockedBy returns an *AttrError if path carries any flag in mask.
func blockedBy(path string, mask attrSet) error {
	a, ok := readAttrs(path)
	if !ok {
		return nil
	}
	switch {
	case a&mask&attrImmutable != 0:
		return &AttrError{Path: path, Attr: "immutable"}
	case a&mask&attrAppendOnly != 0:
		return &AttrError{Path: path, Attr: "append-only"}
	}
	return nil
}

// Both flags stop an entry from being modified, deleted or renamed, and stop
// its owner or mode from changing.
const changeMask = attrImmutable | attrAppendOnly

// checkChangeable is the check for chmod/chown: only the entry's own flags
// matter (its directory's don't).
func checkChangeable(path string) error { return blockedBy(path, changeMask) }

// CheckRemovable returns an *AttrError if path can't be deleted (or moved
// away) because it, or the directory it sits in, is immutable or append-only
// (append-only forbids deleting entries from a directory just as much). It
// only looks at path itself and its parent, not at what a directory
// contains.
func CheckRemovable(path string) error {
	if err := blockedBy(path, changeMask); err != nil {
		return err
	}
	return blockedBy(filepath.Dir(path), changeMask)
}

// checkRenamable is the check for renaming oldPath to newPath: the entry
// must be removable from where it is, an immutable destination directory
// can't receive it (an append-only one can: adding entries is what
// append-only still allows), and an existing destination gets replaced, i.e.
// deleted.
func checkRenamable(oldPath, newPath string) error {
	if err := CheckRemovable(oldPath); err != nil {
		return err
	}
	if err := blockedBy(filepath.Dir(newPath), attrImmutable); err != nil {
		return err
	}
	if _, err := os.Lstat(newPath); err == nil {
		return CheckRemovable(newPath)
	}
	return nil
}

// removeAttrError converts a permission error from a recursive remove into
// the *AttrError explaining it, when the entry that failed (os.RemoveAll
// reports which one) is itself immutable/append-only or sits in such a
// directory — something nested deep inside a folder being deleted, which
// the up-front check on the top-level path can't see. nil for anything else.
func removeAttrError(err error) error {
	var pe *fs.PathError
	if err == nil || !os.IsPermission(err) || !errors.As(err, &pe) {
		return nil
	}
	return CheckRemovable(pe.Path)
}
