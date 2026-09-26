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

package mirror

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"shfm/internal/fileops"
	"shfm/internal/vfs"
)

func localSides(t *testing.T) (src, dst Side, state string) {
	t.Helper()
	tmp := t.TempDir()
	fs := vfs.NewLocalFS("Local", tmp)
	src = Side{FS: fs, Path: filepath.Join(tmp, "src")}
	dst = Side{FS: fs, Path: filepath.Join(tmp, "dst")}
	return src, dst, filepath.Join(tmp, "state", "pair.json")
}

// runCounting runs a generic mirror and returns the result and how many
// operations it reported.
func runCounting(t *testing.T, src, dst Side, state string) (*fileops.Result, int) {
	t.Helper()
	ops := 0
	res := Run(src, dst, Options{StateFile: state}, &fileops.Progress{
		OnItem: func(done, total int, name string, err error) {
			if total > 0 {
				ops = total
			}
		},
	})
	if len(res.Errors) > 0 {
		t.Fatalf("mirror errors: %v", res.Errors)
	}
	return res, ops
}

func TestGenericMirror(t *testing.T) {
	src, dst, state := localSides(t)
	writeTree(t, src.Path, map[string]string{
		"a.txt":         "alpha",
		"sub/b.txt":     "beta",
		"sub/deep/c.md": "gamma",
		"empty/":        "",
		"fileToDir/x":   "now a dir",
		"dirToFile":     "now a file",
	})
	writeTree(t, dst.Path, map[string]string{
		"stale.txt":        "only in dest",
		"sub/old/x.bin":    "stale subtree",
		"zzz/late.txt":     "sorted after stale entries",
		"fileToDir":        "was a file",
		"dirToFile/inner/": "",
	})
	runCounting(t, src, dst, state)
	assertSameTree(t, src.Path, dst.Path)

	// Nothing changed: no operations at all.
	if _, ops := runCounting(t, src, dst, state); ops != 0 {
		t.Fatalf("second run did %d operations, want 0", ops)
	}

	// Same-size change in the source (new mtime) is picked up.
	writeTree(t, src.Path, map[string]string{"a.txt": "ALPHA"})
	future := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(src.Path, "a.txt"), future, future)
	if _, ops := runCounting(t, src, dst, state); ops != 1 {
		t.Fatalf("source change: %d operations, want 1", ops)
	}
	assertSameTree(t, src.Path, dst.Path)

	// A same-size edit made in the destination is overwritten too.
	writeTree(t, dst.Path, map[string]string{"sub/b.txt": "BETA"})
	os.Chtimes(filepath.Join(dst.Path, "sub/b.txt"), future, future)
	if _, ops := runCounting(t, src, dst, state); ops != 1 {
		t.Fatalf("destination edit: %d operations, want 1", ops)
	}
	assertSameTree(t, src.Path, dst.Path)

	// Deletions in the source propagate.
	os.RemoveAll(filepath.Join(src.Path, "sub"))
	runCounting(t, src, dst, state)
	assertSameTree(t, src.Path, dst.Path)
}

func TestGenericMirrorSingleFile(t *testing.T) {
	src, dst, state := localSides(t)
	writeTree(t, filepath.Dir(src.Path), map[string]string{"src": "hello", "dst/": ""})
	runCounting(t, src, dst, state)
	if got, _ := os.ReadFile(dst.Path); string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
	if _, ops := runCounting(t, src, dst, state); ops != 0 {
		t.Fatalf("second run did %d operations, want 0", ops)
	}
}

func TestGenericMirrorMissingSourceTouchesNothing(t *testing.T) {
	src, dst, state := localSides(t)
	writeTree(t, dst.Path, map[string]string{"keep.txt": "precious"})
	for _, useRsync := range []bool{false, true} {
		res := Run(src, dst, Options{StateFile: state, UseRsync: useRsync}, nil)
		if len(res.Errors) == 0 {
			t.Fatalf("rsync=%v: expected an error for a missing source", useRsync)
		}
		if got, _ := os.ReadFile(filepath.Join(dst.Path, "keep.txt")); string(got) != "precious" {
			t.Fatalf("rsync=%v: destination was modified", useRsync)
		}
	}
}

func TestGenericMirrorUnreadableSubfolderIsNotDeleted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	src, dst, state := localSides(t)
	writeTree(t, src.Path, map[string]string{"locked/a.txt": "a", "b.txt": "b"})
	runCounting(t, src, dst, state)
	os.Chmod(filepath.Join(src.Path, "locked"), 0)
	t.Cleanup(func() { os.Chmod(filepath.Join(src.Path, "locked"), 0o755) })

	Run(src, dst, Options{StateFile: state}, nil)
	if got, _ := os.ReadFile(filepath.Join(dst.Path, "locked", "a.txt")); string(got) != "a" {
		t.Fatal("contents of an unreadable source folder were deleted from the destination")
	}
}

func TestGenericMirrorCancel(t *testing.T) {
	src, dst, state := localSides(t)
	writeTree(t, src.Path, map[string]string{"a": "1", "b": "2"})
	res := Run(src, dst, Options{StateFile: state}, &fileops.Progress{Cancelled: func() bool { return true }})
	if !res.Cancelled || res.Done != 0 {
		t.Fatalf("got %+v, want cancelled with nothing done", res)
	}
}
