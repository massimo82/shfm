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
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsOnPseudoFSForProc(t *testing.T) {
	if !isOnPseudoFS("/proc") {
		t.Fatal("/proc must be recognized as a pseudo filesystem")
	}
	if !isOnPseudoFS("/proc/self") {
		t.Error("a subdirectory of /proc must also be recognized as pseudo (longest-prefix match)")
	}
}

// regularDirTestTempDir is t.TempDir(), except rooted under this package's
// own directory instead of the OS temp dir. On quite a few Linux setups
// (containers/CI especially; this sandbox included) the OS temp dir
// (t.TempDir()'s default) is itself tmpfs — a pseudo filesystem by this
// package's own definition — which would make a test asserting "a regular
// directory is NOT pseudo" fail for a reason that has nothing to do with
// the code under test. A checkout's own directory is essentially always on
// a real filesystem, so rooting there sidesteps the issue.
func regularDirTestTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "pseudofs-test-*")
	if err != nil {
		t.Fatalf("couldn't create test dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestIsOnPseudoFSForRegularDir(t *testing.T) {
	dir := regularDirTestTempDir(t)
	if isOnPseudoFS(dir) {
		t.Errorf("a regular temp directory (%s) must NOT be flagged as a pseudo filesystem", dir)
	}
}

func TestMountFSTypeLongestPrefixMatch(t *testing.T) {
	fstype, ok := mountFSType("/proc/self/status")
	if !ok {
		t.Fatal("expected to resolve a filesystem type for a path under /proc")
	}
	if fstype != "proc" {
		t.Errorf("fstype = %q, want %q", fstype, "proc")
	}
}

// TestDirSizeRefusesProc is the direct regression test for the reported
// bug: computing a recursive size for /proc (or anything under it) must
// never walk it and must never return a bogus multi-terabyte number —
// /proc/kcore alone reports a fixed, fictitious 128TiB (2^47 bytes),
// representing virtual address space, not real bytes on disk.
func TestDirSizeRefusesProc(t *testing.T) {
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("/proc not available in this environment")
	}
	fs := NewLocalFS("Local", "/")
	size, count, err := fs.DirSize("/proc")
	if err == nil {
		t.Fatalf("expected DirSize(\"/proc\") to fail rather than compute a (potentially bogus) size; got size=%d count=%d", size, count)
	}
	if !errors.Is(err, errPseudoFS) {
		t.Errorf("expected errPseudoFS, got %v", err)
	}
	if size != 0 || count != 0 {
		t.Errorf("expected zero size/count alongside the error, got size=%d count=%d", size, count)
	}

	const oneTerabyte = 1 << 40
	if size >= oneTerabyte {
		t.Fatalf("DirSize(\"/proc\") returned a suspiciously huge size (%d bytes) — this is exactly the reported bug", size)
	}
}

func TestDirSizeStillWorksOnRegularDirs(t *testing.T) {
	dir := regularDirTestTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := NewLocalFS("Local", dir)
	size, count, err := fs.DirSize(dir)
	if err != nil {
		t.Fatalf("DirSize on a regular directory should not error, got %v", err)
	}
	if size != 5 || count != 1 {
		t.Errorf("size=%d count=%d, want size=5 count=1", size, count)
	}
}
