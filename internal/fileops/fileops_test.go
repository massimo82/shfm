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

package fileops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shfm/internal/vfs"
)

// localDeviceTempDir is t.TempDir(), except rooted under this package's own
// directory rather than the OS temp dir. It matters here specifically
// because trash.MoveToTrash compares device IDs (sameDevice) to decide
// between the home trash and a "top directory" trash can elsewhere on the
// same filesystem as the file being trashed — t.TempDir()'s default
// location (often tmpfs, e.g. under /tmp) can land on a different device
// than $HOME, which would silently reroute trashing through a code path
// this test isn't exercising. A checkout's own directory is on the same
// disk as $HOME in the realistic case this test cares about.
func localDeviceTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "fileops-test-*")
	if err != nil {
		t.Fatalf("couldn't create test dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// unwritableDir returns a directory (owned by the current user — no root
// needed) with its own write bit removed: even the owner can't add/remove/
// rename entries inside it without it, which is enough to make a plain
// os.Remove/os.Rename on something inside genuinely fail with EACCES, real
// permission semantics, no root-owned fixture required.
func unwritableDir(t *testing.T, parent string) string {
	t.Helper()
	dir := filepath.Join(parent, "locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) }) // let cleanup remove its contents afterwards
	return dir
}

// TestDeletePermissionDeniedTrashFallsBackToElevatedRemove is a real,
// non-mocked, end-to-end exercise of the full chain this package and
// internal/vfs implement together: Delete(useTrash: true) on a file it
// can't remove from its directory (a permission problem, simulated here via
// an unwritable directory rather than actual root ownership — the two are
// indistinguishable to the code under test, which only ever sees EACCES/
// EPERM) must first try trashing it, notice that failed for lack of
// permission, fall back to a permanent delete, and have THAT attempt
// elevation via pkexec in turn (internal/vfs's own job, see
// elevate_linux_test.go for that layer's dedicated tests).
//
// This machine has no interactive PolicyKit agent available (a sandboxed/
// headless environment), so the real pkexec — deliberately not faked here,
// since faking it would require an exported testing seam into internal/vfs
// this package has no legitimate reason to use outside tests — fails fast
// and predictably (observed: exit 127, "no controlling terminal") rather
// than hanging. That failure is itself the useful signal: it's only
// reachable at all if every step up to and including the real pkexec
// invocation ran correctly, and its specific wording ("pkexec") confirms
// which step failed.
func TestDeletePermissionDeniedTrashFallsBackToElevatedRemove(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions wouldn't block us")
	}
	if _, err := os.UserHomeDir(); err != nil {
		t.Skip("no $HOME in this environment")
	}

	trashRoot := localDeviceTempDir(t)
	t.Setenv("XDG_DATA_HOME", trashRoot)

	parent := localDeviceTempDir(t)
	dir := unwritableDir(t, parent)
	path := filepath.Join(dir, "f.txt")
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}

	fs := vfs.NewLocalFS("Local", parent)
	res := Delete([]Item{{FS: fs, Path: path}}, true, nil)

	if len(res.Errors) != 1 {
		t.Fatalf("expected exactly one error (this environment can't complete a real pkexec auth), got %d: %v", len(res.Errors), res.Errors)
	}
	if !strings.Contains(res.Errors[0].Error(), "pkexec") {
		t.Errorf("expected the error to show the chain reached pkexec elevation, got: %v", res.Errors[0])
	}

	// The original must still be exactly where it was: a failed delete
	// (even one that tried and failed to elevate) must never silently drop
	// the file.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("original file should still exist after a failed delete, stat error: %v", err)
	}

	// No orphaned trash entry left behind (see the MoveToTrash cleanup
	// fix in internal/trash/trash.go this test also covers): the trash
	// "files" directory must be empty.
	filesDir := filepath.Join(trashRoot, "Trash", "files")
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		t.Fatalf("couldn't read trash files dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no orphaned entries in the trash, found %v", entries)
	}
	infoDir := filepath.Join(trashRoot, "Trash", "info")
	infoEntries, err := os.ReadDir(infoDir)
	if err != nil {
		t.Fatalf("couldn't read trash info dir: %v", err)
	}
	if len(infoEntries) != 0 {
		t.Errorf("expected no orphaned .trashinfo entries, found %v", infoEntries)
	}
}

// TestDeletePermanentSucceedsNormallyWithoutElevation is a control: a
// perfectly ordinary permanent delete (no trash, no permission problem)
// must behave exactly as before this feature existed.
func TestDeletePermanentSucceedsNormallyWithoutElevation(t *testing.T) {
	dir := localDeviceTempDir(t)
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := vfs.NewLocalFS("Local", dir)
	res := Delete([]Item{{FS: fs, Path: path}}, false, nil)
	if len(res.Errors) != 0 {
		t.Fatalf("expected no errors, got %v", res.Errors)
	}
	if res.Done != 1 {
		t.Errorf("expected 1 item done, got %d", res.Done)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected the file to be gone, stat error: %v", err)
	}
}
