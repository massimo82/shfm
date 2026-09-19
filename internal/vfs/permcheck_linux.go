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
	"path/filepath"
	"syscall"
)

// This file predicts, from the standard Unix owner/group/other permission
// model, whether a chmod/chown/remove/rename on the local filesystem would
// fail for the calling (non-root) user — so elevate_linux.go's callers can
// go straight to pkexec instead of first burning a doomed plain attempt
// (and, for delete/move specifically, potentially doing partial damage —
// e.g. a recursive delete that removes everything it can before finally
// hitting the one file it can't, then needing to redo the whole thing
// elevated anyway). This is a *prediction*, not an authority: the actual
// syscall is still what decides, and every caller keeps its existing
// reactive elevateIfPermissionError fallback for whatever this prediction
// gets wrong (ACLs, LSMs, a stat that raced with a concurrent chmod
// elsewhere, ...). Immutable/append-only attributes, which even root can't
// override, are not one of those: attrs.go detects them beforehand.

// isRoot reports whether shfm itself is already running as root, in which
// case none of this applies: every permission check below would be
// trivially satisfied, and there's nothing pkexec could add.
func isRoot() bool { return os.Geteuid() == 0 }

// identity is the calling process's own credentials, gathered once per
// prediction: effective uid, effective gid, and the full set of group
// memberships (egid plus every supplementary group) — everything the
// kernel itself consults to resolve owner/group/other.
type identity struct {
	euid   uint32
	egid   uint32
	groups map[uint32]bool
}

func currentIdentity() identity {
	id := identity{
		euid:   uint32(os.Geteuid()),
		egid:   uint32(os.Getegid()),
		groups: map[uint32]bool{},
	}
	id.groups[id.egid] = true
	if gids, err := os.Getgroups(); err == nil {
		for _, g := range gids {
			id.groups[uint32(g)] = true
		}
	}
	return id
}

// statOwnerGroupMode returns path's owner uid, group gid and full mode
// (including the setuid/setgid/sticky bits os.FileMode also carries) —
// or ok=false if that can't be determined (path doesn't exist, or its
// Sys() isn't Unix-shaped), in which case callers should skip prediction
// entirely and let the plain attempt run, reactive fallback and all,
// exactly as before this file existed.
func statOwnerGroupMode(path string) (uid, gid uint32, mode os.FileMode, ok bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, 0, false
	}
	st, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, 0, false
	}
	return uint32(st.Uid), uint32(st.Gid), info.Mode(), true
}

// canWrite reports whether id would be granted write permission on an
// object owned by (uid, gid) with the given mode, per the kernel's own
// resolution order: the *first* applicable bucket wins, even if a later
// one would've been more permissive — an owner whose own write bit is
// unset is denied even when the group or other bits would allow it.
func canWrite(uid, gid uint32, mode os.FileMode, id identity) bool {
	switch {
	case id.euid == uid:
		return mode&0o200 != 0
	case id.groups[gid]:
		return mode&0o020 != 0
	default:
		return mode&0o002 != 0
	}
}

// chmodNeeds is chmodNeedsElevation's actual decision, factored out to take
// the file's owner uid directly (rather than a path to stat) so it's unit-
// testable against synthetic ownership without needing a root-owned
// fixture. Unlike read/write access, chmod isn't governed by the owner/
// group/other bits at all — only a file's *owner* may ever change its
// mode, full stop, regardless of what the current permission bits happen
// to be.
func chmodNeeds(fileUID uint32, id identity) bool {
	return id.euid != fileUID
}

// chmodNeedsElevation predicts whether os.Chmod(path, ...) would fail for
// the calling (non-root) user — see chmodNeeds.
func chmodNeedsElevation(path string) bool {
	if isRoot() {
		return false
	}
	uid, _, _, ok := statOwnerGroupMode(path)
	if !ok {
		return false
	}
	return chmodNeeds(uid, currentIdentity())
}

// chownNeeds is chownNeedsElevation's actual decision, factored out to
// take the file's current owner/group directly (rather than a path to
// stat) so it's unit-testable against synthetic ownership without needing
// a root-owned fixture. Changing to a different uid always needs root —
// regular users can never take or give away file ownership by uid on
// Linux, no exceptions. Changing only the gid is allowed without root if
// the caller already owns the file *and* is a member of the target group
// (the standard "hand this file to a group I'm in" case); newUID/newGID of
// -1 (matching os.Chown's own "leave this one alone" convention) are
// treated as "no change requested" for that half.
func chownNeeds(fileUID, fileGID uint32, newUID, newGID int, id identity) bool {
	if newUID >= 0 && uint32(newUID) != fileUID {
		return true
	}
	if newGID >= 0 && uint32(newGID) != fileGID {
		return id.euid != fileUID || !id.groups[uint32(newGID)]
	}
	return false
}

// chownNeedsElevation predicts whether os.Chown(path, newUID, newGID)
// would fail for the calling (non-root) user — see chownNeeds.
func chownNeedsElevation(path string, newUID, newGID int) bool {
	if isRoot() {
		return false
	}
	uid, gid, _, ok := statOwnerGroupMode(path)
	if !ok {
		return false
	}
	return chownNeeds(uid, gid, newUID, newGID, currentIdentity())
}

// removeOrRenameNeeds is removeOrRenameNeedsElevation's actual decision,
// factored out to take the parent directory's owner/group/mode and the
// target's own owner uid (if known) directly, rather than paths to stat,
// so it's unit-testable against synthetic ownership without needing a
// root-owned fixture. Removing/renaming an entry — altering the
// *directory*, not the file's own content — is governed by the parent's
// write permission for the caller, and, if the parent has the sticky bit
// set (like /tmp, mode 1777), also by ownership of the entry itself: a
// sticky directory restricts deletion/renaming to an entry's own owner (or
// the directory's owner) even when the directory is otherwise
// world-writable — without this check, "can write the parent" alone would
// wrongly predict success in exactly the one directory (/tmp) shfm users
// are likeliest to actually hit this in.
func removeOrRenameNeeds(parentUID, parentGID uint32, parentMode os.FileMode, fileUID uint32, fileUIDKnown bool, id identity) bool {
	if !canWrite(parentUID, parentGID, parentMode, id) {
		return true
	}
	if parentMode&os.ModeSticky != 0 && fileUIDKnown {
		if id.euid != fileUID && id.euid != parentUID {
			return true
		}
	}
	return false
}

// removeOrRenameNeedsElevation predicts whether removing or renaming path
// would fail for the calling (non-root) user — see removeOrRenameNeeds.
func removeOrRenameNeedsElevation(path string) bool {
	if isRoot() {
		return false
	}
	pUID, pGID, pMode, ok := statOwnerGroupMode(filepath.Dir(path))
	if !ok {
		return false
	}
	fUID, _, _, fOK := statOwnerGroupMode(path)
	return removeOrRenameNeeds(pUID, pGID, pMode, fUID, fOK, currentIdentity())
}

// renameNeedsElevation is removeOrRenameNeedsElevation extended to also
// cover the destination when it's in a different directory than the
// source (a cross-directory move, not just an in-place rename) — adding
// an entry there needs its own write permission check, independent of the
// source side (and never a sticky-bit concern: sticky restricts removing/
// renaming *existing* entries, not creating new ones).
func renameNeedsElevation(oldPath, newPath string) bool {
	if isRoot() {
		return false
	}
	if removeOrRenameNeedsElevation(oldPath) {
		return true
	}
	oldParent, newParent := filepath.Dir(oldPath), filepath.Dir(newPath)
	if oldParent == newParent {
		return false
	}
	dUID, dGID, dMode, ok := statOwnerGroupMode(newParent)
	if !ok {
		return false
	}
	return !canWrite(dUID, dGID, dMode, currentIdentity())
}
