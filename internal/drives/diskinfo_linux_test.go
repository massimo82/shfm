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

import "testing"

func TestDiskBaseName(t *testing.T) {
	cases := map[string]string{
		"/dev/sda1":        "sda",
		"/dev/sda":         "sda",
		"/dev/sdb2":        "sdb",
		"/dev/nvme0n1p1":   "nvme0n1",
		"/dev/nvme0n1":     "nvme0n1",
		"/dev/nvme1n1p12":  "nvme1n1",
		"/dev/mmcblk0p1":   "mmcblk0",
		"/dev/mmcblk0":     "mmcblk0",
		"/dev/vda2":        "vda",
		"nfs-server:/data": "nfs-server:/data", // not a /dev path: passed through, harmless
	}
	for in, want := range cases {
		if got := diskBaseName(in); got != want {
			t.Errorf("diskBaseName(%q) = %q, want %q", in, got, want)
		}
	}
}
