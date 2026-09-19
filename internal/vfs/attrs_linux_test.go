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

// On a plain file the real ioctl either works and reports no restricting
// flags, or isn't supported by the filesystem; it must never invent flags,
// and it must refuse to query anything but regular files and directories.
// (Setting a real immutable flag needs root, so that half is exercised
// through the substituted readAttrs in attrs_test.go instead.)
func TestReadAttrsPlatform(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{file, dir} {
		if a, ok := readAttrsPlatform(p); ok && a != 0 {
			t.Errorf("%s: flags %v reported on a plain entry", p, a.names())
		}
	}

	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, ok := readAttrsPlatform(link); ok {
		t.Error("a symlink has no flags of its own and must not be queried")
	}
	if _, ok := readAttrsPlatform(filepath.Join(dir, "missing")); ok {
		t.Error("a missing path must be reported as unknown")
	}
}
