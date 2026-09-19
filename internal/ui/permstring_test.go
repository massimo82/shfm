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
	"os"
	"testing"
)

func TestPermString(t *testing.T) {
	cases := []struct {
		mode os.FileMode
		want string
	}{
		{0o755, "rwxr-xr-x"},
		{0o644, "rw-r--r--"},
		{0o600, "rw-------"},
		{0o777, "rwxrwxrwx"},
		{0o000, "---------"},
		{0o421, "r---w---x"},
		// The type bits (directory, symlink...) must not leak into the
		// permission string: only the low 9 bits matter.
		{os.ModeDir | 0o755, "rwxr-xr-x"},
		{os.ModeSymlink | 0o777, "rwxrwxrwx"},
	}
	for _, c := range cases {
		got := permString(c.mode)
		if got != c.want {
			t.Errorf("permString(%o) = %q, want %q", c.mode, got, c.want)
		}
	}
}
