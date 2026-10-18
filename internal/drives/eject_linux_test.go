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

package drives

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSystemDisk makes SystemDiskName see the root filesystem on
// /dev/shfmtest1, for one test, and returns that disk: a name no machine
// has, so that a refusal that doesn't happen acts on nothing real. Only
// the system disk is told from these synthetic mounts: unmounting still
// reads the real ones, where that disk never appears.
func fakeSystemDisk(t *testing.T) string {
	t.Helper()
	mf := filepath.Join(t.TempDir(), "mounts")
	if err := os.WriteFile(mf, []byte("/dev/shfmtest1 / ext4 rw 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := mountsFile
	mountsFile = mf
	t.Cleanup(func() { mountsFile = old })
	if sys, err := SystemDiskName(); err != nil || sys != "shfmtest" {
		t.Fatalf("synthetic system disk not seen: %q, %v", sys, err)
	}
	return "/dev/shfmtest"
}

// Eject refuses the system disk before unmounting anything on it.
func TestEjectRefusesSystemDisk(t *testing.T) {
	disk := fakeSystemDisk(t)
	off, err := Eject(disk)
	if err == nil || off || !strings.Contains(err.Error(), "holds the system") {
		t.Fatalf("Eject(system disk) = %v, %v; want a refusal", off, err)
	}
}
