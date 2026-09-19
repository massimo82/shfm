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
	"testing"
)

func idOf(euid, egid uint32, groups ...uint32) identity {
	id := identity{euid: euid, egid: egid, groups: map[uint32]bool{egid: true}}
	for _, g := range groups {
		id.groups[g] = true
	}
	return id
}

// --- canWrite: pure logic, synthetic identities, no filesystem -------------

func TestCanWriteOwnerBucket(t *testing.T) {
	me := idOf(1000, 1000)
	if !canWrite(1000, 2000, 0o600, me) {
		t.Error("owner with write bit set should be granted write")
	}
	if canWrite(1000, 2000, 0o400, me) {
		t.Error("owner without write bit should be denied, even though it's their file")
	}
}

func TestCanWriteGroupBucketOnlyWhenMember(t *testing.T) {
	member := idOf(1000, 1000, 2000)
	nonMember := idOf(1000, 1000)
	if !canWrite(9999, 2000, 0o060, member) {
		t.Error("group member with group write bit set should be granted write")
	}
	if canWrite(9999, 2000, 0o060, nonMember) {
		t.Error("non-member must fall through to the other bucket, not the group bucket")
	}
}

func TestCanWriteOtherBucket(t *testing.T) {
	stranger := idOf(1000, 1000)
	if !canWrite(9999, 8888, 0o006, stranger) {
		t.Error("neither owner nor group member: other write bit should grant write")
	}
	if canWrite(9999, 8888, 0o060, stranger) {
		t.Error("neither owner nor group member: group write bit must not apply")
	}
}

func TestCanWriteFirstMatchingBucketWinsEvenIfMorePermissiveLater(t *testing.T) {
	// This is the case that would trip up a naive "OR together all the bits
	// I might qualify for" implementation: the caller IS the owner, so only
	// the owner bucket's bit matters — group/other being wide open must not
	// grant access the owner bucket itself denies.
	me := idOf(1000, 1000, 2000)
	if canWrite(1000, 2000, 0o066, me) {
		t.Error("owner bucket denies (no owner-write bit): group/other write bits must not override that")
	}
}

// --- chmodNeeds --------------------------------------------------------

func TestChmodNeeds(t *testing.T) {
	me := idOf(1000, 1000)
	if chmodNeeds(1000, me) {
		t.Error("owner chmodding their own file: should not need elevation")
	}
	if !chmodNeeds(9999, me) {
		t.Error("non-owner chmodding a file: should need elevation, regardless of mode bits (chmod ignores them)")
	}
}

// --- chownNeeds ----------------------------------------------------------

func TestChownNeedsUIDChangeAlwaysElevates(t *testing.T) {
	me := idOf(1000, 1000)
	// Even chowning a file already owned to the caller's OWN uid, when the
	// caller doesn't already own it, is a uid change from the file's
	// perspective and needs root.
	if !chownNeeds(9999, 1000, 1000, -1, me) {
		t.Error("uid change (even to caller's own uid, on a file they don't own) should need elevation")
	}
}

func TestChownNeedsNoActualChangeNeedsNothing(t *testing.T) {
	me := idOf(1000, 1000)
	if chownNeeds(1000, 1000, 1000, 1000, me) {
		t.Error("chown to the exact same uid/gid the file already has should not need elevation")
	}
	if chownNeeds(1000, 1000, -1, -1, me) {
		t.Error("chown with both uid and gid left alone (-1) should not need elevation")
	}
}

func TestChownNeedsGidOnlyChangeAllowedWhenOwnerAndMember(t *testing.T) {
	me := idOf(1000, 1000, 2000)
	if chownNeeds(1000, 1000, -1, 2000, me) {
		t.Error("gid-only change to a group the owning caller belongs to should not need elevation")
	}
}

func TestChownNeedsGidOnlyChangeElevatesWhenNotOwner(t *testing.T) {
	me := idOf(1000, 1000, 2000)
	if !chownNeeds(9999, 1000, -1, 2000, me) {
		t.Error("gid-only change on a file the caller doesn't own should need elevation, even if a member of the target group")
	}
}

func TestChownNeedsGidOnlyChangeElevatesWhenNotMember(t *testing.T) {
	me := idOf(1000, 1000)
	if !chownNeeds(1000, 1000, -1, 2000, me) {
		t.Error("gid-only change to a group the owning caller does NOT belong to should need elevation")
	}
}

// --- removeOrRenameNeeds ---------------------------------------------------

func TestRemoveOrRenameNeedsParentNotWritable(t *testing.T) {
	me := idOf(1000, 1000)
	if !removeOrRenameNeeds(1000, 1000, 0o555, 0, false, me) {
		t.Error("owner of the parent dir but no write bit: should need elevation")
	}
}

func TestRemoveOrRenameNeedsParentWritableNoSticky(t *testing.T) {
	me := idOf(1000, 1000)
	if removeOrRenameNeeds(1000, 1000, 0o755, 9999, true, me) {
		t.Error("writable parent, no sticky bit: should not need elevation regardless of who owns the file")
	}
}

func TestRemoveOrRenameNeedsStickyOwnFile(t *testing.T) {
	me := idOf(1000, 1000)
	if removeOrRenameNeeds(9999, 9999, os.ModeSticky|0o777, 1000, true, me) {
		t.Error("sticky world-writable dir, caller owns the file: should not need elevation")
	}
}

func TestRemoveOrRenameNeedsStickyDirOwner(t *testing.T) {
	me := idOf(1000, 1000)
	if removeOrRenameNeeds(1000, 1000, os.ModeSticky|0o777, 9999, true, me) {
		t.Error("sticky world-writable dir, caller owns the DIRECTORY (not the file): should not need elevation")
	}
}

func TestRemoveOrRenameNeedsStickyNeitherOwner(t *testing.T) {
	me := idOf(1000, 1000)
	if !removeOrRenameNeeds(9999, 9999, os.ModeSticky|0o777, 8888, true, me) {
		t.Error("sticky world-writable dir, caller owns neither dir nor file: should need elevation (this is the /tmp case)")
	}
}

func TestRemoveOrRenameNeedsStickyFileOwnerUnknown(t *testing.T) {
	me := idOf(1000, 1000)
	// fileUIDKnown=false (e.g. the target's own Lstat failed, perhaps it
	// was removed by something else in a race): can't evaluate the sticky
	// exception, so this must not force elevation just because it can't be
	// sure — matches the "when in doubt, let the plain attempt's own
	// reactive fallback sort it out" philosophy documented on
	// statOwnerGroupMode.
	if removeOrRenameNeeds(9999, 9999, os.ModeSticky|0o777, 0, false, me) {
		t.Error("sticky dir with unknown file ownership should not force elevation")
	}
}

// --- real (no-root-needed) integration tests -------------------------------

func TestChmodNeedsElevationRealOwnFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if chmodNeedsElevation(path) {
		t.Error("chmodding a file I own should not predict elevation")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Errorf("sanity check: the real chmod should also succeed: %v", err)
	}
}

func TestChownNeedsElevationRealDifferentUID(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherUID := os.Geteuid() + 1
	if !chownNeedsElevation(path, otherUID, os.Getegid()) {
		t.Error("chowning to a different uid should predict elevation")
	}
	if err := os.Chown(path, otherUID, os.Getegid()); !os.IsPermission(err) {
		t.Errorf("sanity check: the real chown should fail with a permission error, got %v", err)
	}
}

func TestChownNeedsElevationRealSameUIDGroupIAmMemberOf(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	supplementary, err := os.Getgroups()
	if err != nil || len(supplementary) == 0 {
		t.Skip("no supplementary groups available to test the membership case with")
	}
	targetGID := supplementary[0]

	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if chownNeedsElevation(path, os.Geteuid(), targetGID) {
		t.Error("gid-only change to a group I'm a member of, on a file I own, should not predict elevation")
	}
	if err := os.Chown(path, os.Geteuid(), targetGID); err != nil {
		t.Errorf("sanity check: the real chown should also succeed: %v", err)
	}
}

func TestRemoveOrRenameNeedsElevationRealWritableDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if removeOrRenameNeedsElevation(path) {
		t.Error("a plain writable directory should not predict elevation")
	}
	if err := os.Remove(path); err != nil {
		t.Errorf("sanity check: the real remove should also succeed: %v", err)
	}
}

func TestRemoveOrRenameNeedsElevationRealUnwritableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	if !removeOrRenameNeedsElevation(path) {
		t.Error("a directory without write permission should predict elevation")
	}
	if err := os.Remove(path); !os.IsPermission(err) {
		t.Errorf("sanity check: the real remove should fail with a permission error, got %v", err)
	}
}

func TestRemoveOrRenameNeedsElevationRealStickyOwnFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "sticky")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o1777); err != nil { // world-writable + sticky, like /tmp
		t.Fatal(err)
	}
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if removeOrRenameNeedsElevation(path) {
		t.Error("removing my own file from a sticky dir I also own should not predict elevation")
	}
	if err := os.Remove(path); err != nil {
		t.Errorf("sanity check: the real remove should also succeed: %v", err)
	}
}

func TestRenameNeedsElevationCrossDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root")
	}
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	for _, d := range []string{srcDir, dstDir} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldPath := filepath.Join(srcDir, "f.txt")
	if err := os.WriteFile(oldPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Both writable: should not need elevation, and the real rename works.
	newPath := filepath.Join(dstDir, "f.txt")
	if renameNeedsElevation(oldPath, newPath) {
		t.Error("both directories writable: should not predict elevation")
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("sanity check: the real rename should also succeed: %v", err)
	}

	// Lock down the destination and try moving it back: must now predict
	// elevation, and the real rename genuinely fails.
	if err := os.Chmod(srcDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(srcDir, 0o755) })

	backPath := filepath.Join(srcDir, "f.txt")
	if !renameNeedsElevation(newPath, backPath) {
		t.Error("destination directory not writable: should predict elevation")
	}
	if err := os.Rename(newPath, backPath); !os.IsPermission(err) {
		t.Errorf("sanity check: the real rename should fail with a permission error, got %v", err)
	}
}
