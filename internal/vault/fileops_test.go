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

//go:build vault

package vault

import (
	"os"
	"path/filepath"
	"testing"

	"shfm/internal/fileops"
	"shfm/internal/vfs"
)

// shfm's own copy and move operations work into, inside and out of a
// vault, folders included.
func TestFileops(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		local := vfs.NewLocalFS("local", "/")
		_, _, _, fs := newVault(t, opts)
		src := t.TempDir()
		os.MkdirAll(filepath.Join(src, "tree", "sub"), 0o755)
		os.WriteFile(filepath.Join(src, "tree", "a.txt"), []byte("A"), 0o644)
		big := randBytes(5*chunkSize + 3)
		os.WriteFile(filepath.Join(src, "tree", "sub", "big.bin"), big, 0o644)

		check := func(r *fileops.Result) {
			t.Helper()
			if len(r.Errors) > 0 {
				t.Fatalf("errors: %v", r.Errors)
			}
		}
		check(fileops.Copy([]fileops.Item{{FS: local, Path: filepath.Join(src, "tree")}}, fs, "/", nil))
		if got := readFile(t, fs, "/tree/sub/big.bin"); string(got) != string(big) {
			t.Fatal("copied into the vault: content differs")
		}

		fs.Mkdir("/elsewhere")
		check(fileops.Move([]fileops.Item{{FS: fs, Path: "/tree"}}, fs, "/elsewhere", nil))
		if _, err := fs.Stat("/tree"); err == nil {
			t.Fatal("moved folder still at its old place")
		}

		out := t.TempDir()
		check(fileops.Copy([]fileops.Item{{FS: fs, Path: "/elsewhere/tree"}}, local, out, nil))
		got, err := os.ReadFile(filepath.Join(out, "tree", "sub", "big.bin"))
		if err != nil || string(got) != string(big) {
			t.Fatalf("copied out of the vault: %v", err)
		}
		if a, _ := os.ReadFile(filepath.Join(out, "tree", "a.txt")); string(a) != "A" {
			t.Fatalf("a.txt: %q", a)
		}

		check(fileops.Delete([]fileops.Item{{FS: fs, Path: "/elsewhere"}}, false, nil))
		if got := names(t, fs, "/"); len(got) != 0 {
			t.Fatalf("after Delete: %v", got)
		}
	})
}
