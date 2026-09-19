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

import "testing"

func TestDriveDisplayName(t *testing.T) {
	cases := []struct{ vendor, model, want string }{
		{"Samsung", "SSD 860 EVO 1TB", "Samsung SSD 860 EVO 1TB "},
		{"", "SSD 860 EVO 1TB", "SSD 860 EVO 1TB "},
		{"Samsung", "", "Samsung "},
		{"", "", ""},
		{"  ", "  ", ""},
	}
	for _, c := range cases {
		if got := driveDisplayName(c.vendor, c.model); got != c.want {
			t.Errorf("driveDisplayName(%q, %q) = %q, want %q", c.vendor, c.model, got, c.want)
		}
	}
}

func TestSourceMenuRowsInsertsSeparatorsBetweenGroups(t *testing.T) {
	entries := []sourceMenuEntry{
		{kind: "local"},             // row 0
		{kind: "local"},             // row 1
		{kind: "removable-mounted"}, // row 3 (blank at 2)
		{kind: "format-request"},    // row 4 (same group as removable-mounted)
		{kind: "mtp"},               // row 6 (blank at 5)
		{kind: "new-smb"},           // row 8 (blank at 7)
		{kind: "new-nfs"},           // row 9 (same group as new-smb)
	}
	want := []int{0, 1, 3, 4, 6, 8, 9}
	got := sourceMenuRows(entries)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row[%d] = %d, want %d (full: %v)", i, got[i], want[i], got)
		}
	}
}

func TestSourceMenuRowsNoSeparatorForSingleGroup(t *testing.T) {
	entries := []sourceMenuEntry{{kind: "local"}, {kind: "local"}, {kind: "local"}}
	got := sourceMenuRows(entries)
	want := []int{0, 1, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}
