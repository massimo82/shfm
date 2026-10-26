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
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestLocalFSListFlagsUnreadableMetadata: a folder readable but not
// searchable (r without x) lists its names, but lstat on each fails. The
// entries must still appear, with their type from d_type and StatErr set,
// rather than as zero-valued look-alikes of real empty files.
func TestLocalFSListFlagsUnreadableMetadata(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the missing x permission")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	entries, err := NewLocalFS("Local", "/").List(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range entries {
		got[e.Name] = e
		if !errors.Is(e.StatErr, fs.ErrPermission) {
			t.Errorf("%s: StatErr = %v, want permission denied", e.Name, e.StatErr)
		}
	}
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %+v", entries)
	}
	if !got["sub"].IsDir || got["file"].IsDir || !got["link"].IsSymlink {
		t.Errorf("types from d_type lost: %+v", entries)
	}

	// The same folder made searchable again: no StatErr.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err = NewLocalFS("Local", "/").List(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.StatErr != nil {
			t.Errorf("%s: unexpected StatErr %v", e.Name, e.StatErr)
		}
	}
}
