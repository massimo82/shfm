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

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"shfm/internal/vfs"
)

// dirSizeTestTempDir is t.TempDir(), except rooted under this package's own
// directory instead of the OS temp dir. LocalFS.DirSize refuses to walk a
// pseudo/virtual filesystem (tmpfs, procfs, ...) — see
// internal/vfs/pseudofs_linux.go — and on quite a few Linux setups
// (containers/CI especially; this sandbox included) the OS temp dir
// (t.TempDir()'s default) is tmpfs, which would make DirSize always refuse
// and a test waiting on its result hang until timeout with no indication
// why. A checkout's own directory is essentially always on a real
// filesystem (that's where the git history/build artifacts/etc. that
// actually matters lives), so rooting there sidesteps the issue and still
// exercises the genuine, real-filesystem code path everywhere.
func dirSizeTestTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", "dirsize-test-*")
	if err != nil {
		t.Fatalf("couldn't create test dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// skipIfDirSizeUnsupported probes sizer.DirSize(path) once, synchronously,
// and skips the test if it errors — a last-resort safety net for the rare
// case where even dirSizeTestTempDir's location turns out to be on a
// pseudo/virtual filesystem, turning what would otherwise be a confusing
// timeout into an honest, immediate skip.
func skipIfDirSizeUnsupported(t *testing.T, sizer vfs.DirSizer, path string) {
	t.Helper()
	if _, _, err := sizer.DirSize(path); err != nil {
		t.Skipf("DirSize unsupported for %s in this environment (likely a pseudo/virtual filesystem, e.g. tmpfs under /tmp): %v", path, err)
	}
}

// TestDirSizeIsComputedAsynchronously builds a folder with a subfolder
// containing enough files that a synchronous recursive size computation
// would take a noticeable amount of time, then verifies that:
//  1. Load() returns quickly regardless (it doesn't block on the scan),
//     with the subfolder initially showing SizePending;
//  2. the correct size eventually arrives via dirSizeMsg and gets applied
//     by the same code path Update() uses.
func TestDirSizeIsComputedAsynchronously(t *testing.T) {
	dir := dirSizeTestTempDir(t)
	sub := filepath.Join(dir, "big")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	const fileCount = 500
	const fileSize = 1000
	var wantTotal int64
	payload := make([]byte, fileSize)
	for i := 0; i < fileCount; i++ {
		name := filepath.Join(sub, fmt.Sprintf("f%04d.bin", i))
		if err := os.WriteFile(name, payload, 0o644); err != nil {
			t.Fatal(err)
		}
		wantTotal += fileSize
	}

	fs := vfs.NewLocalFS("Local", dir)
	skipIfDirSizeUnsupported(t, fs, sub)

	m := newTestModel()
	sizeCh := make(chan dirSizeMsg, 16)

	start := time.Now()
	p := NewPane(fs, dir, false, m.active, sizeCh)
	elapsed := time.Since(start)

	// Sanity: a synchronous walk of 500 files would typically take at
	// least a few milliseconds; this is a soft check that Load() isn't
	// doing the walk itself inline, not a hard performance assertion (to
	// avoid a flaky test on slow CI machines).
	if elapsed > 200*time.Millisecond {
		t.Errorf("Load() took %v — looks like it might be blocking on the size scan instead of returning immediately", elapsed)
	}

	var sub_ vfs.Entry
	found := false
	for _, e := range p.Entries {
		if e.Name == "big" {
			sub_, found = e, true
		}
	}
	if !found {
		t.Fatal("expected to find the 'big' subfolder in the listing")
	}
	if sub_.Size != SizePending {
		t.Errorf("expected the subfolder's size to be SizePending right after Load(), got %d", sub_.Size)
	}
	if !p.DirSizesSupported {
		t.Error("expected DirSizesSupported to be true for a local filesystem")
	}

	// Wait for the background goroutine's result and apply it exactly the
	// way Model.Update() does.
	select {
	case msg := <-sizeCh:
		if msg.name != "big" || msg.dirPath != dir {
			t.Fatalf("unexpected message: %+v", msg)
		}
		if msg.size != wantTotal {
			t.Errorf("computed size = %d, want %d", msg.size, wantTotal)
		}
		if msg.itemCount != fileCount {
			t.Errorf("computed item count = %d, want %d", msg.itemCount, fileCount)
		}
		m.panes[m.active] = p
		m.handleDirSizeMsg(msg)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the background size computation")
	}

	for _, e := range m.panes[m.active].Entries {
		if e.Name == "big" && e.Size != wantTotal {
			t.Errorf("after handleDirSizeMsg, entry size = %d, want %d", e.Size, wantTotal)
		}
		if e.Name == "big" && e.ItemCount != fileCount {
			t.Errorf("after handleDirSizeMsg, entry item count = %d, want %d", e.ItemCount, fileCount)
		}
	}
}

// TestDirSizeItemCountIncludesFilesAndSubfolders verifies that the
// recursive item count covers both files and subfolders (nested at any
// depth), not just top-level files.
func TestDirSizeItemCountIncludesFilesAndSubfolders(t *testing.T) {
	dir := dirSizeTestTempDir(t)
	root := filepath.Join(dir, "mixed")
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// root: 2 files + 1 subfolder ("nested") = 3 direct entries
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	// nested: 2 more files
	if err := os.WriteFile(filepath.Join(nested, "c.txt"), []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "d.txt"), []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}
	// total: a.txt, b.txt, nested/, nested/c.txt, nested/d.txt = 5 items

	fs := vfs.NewLocalFS("Local", dir)
	skipIfDirSizeUnsupported(t, fs, root)

	sizeCh := make(chan dirSizeMsg, 4)
	NewPane(fs, dir, false, 0, sizeCh)

	select {
	case msg := <-sizeCh:
		if msg.name != "mixed" {
			t.Fatalf("unexpected message: %+v", msg)
		}
		if msg.itemCount != 5 {
			t.Errorf("item count = %d, want 5 (2 files + 1 subfolder + 2 nested files)", msg.itemCount)
		}
		if msg.size != 4 { // a.txt+b.txt+c.txt+d.txt = 4 bytes total
			t.Errorf("size = %d, want 4", msg.size)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the background size computation")
	}
}

// TestDirSizeMsgDiscardedIfNavigatedAway verifies that a size result for a
// folder listing the pane has since navigated away from is safely ignored
// instead of corrupting the (unrelated) current listing.
func TestDirSizeMsgDiscardedIfNavigatedAway(t *testing.T) {
	m := newTestModel()
	original := m.panes[m.active].Path

	// Simulate a stale result for a path that is no longer current.
	m.handleDirSizeMsg(dirSizeMsg{
		paneIndex: m.active,
		dirPath:   "/some/other/path/we/are/not/showing",
		name:      "whatever",
		size:      12345,
	})

	if m.panes[m.active].Path != original {
		t.Fatal("handling a stale size message must not change the pane's path")
	}
	for _, e := range m.panes[m.active].Entries {
		if e.Name == "whatever" {
			t.Error("a stale size message must not inject entries into the current listing")
		}
	}
}
