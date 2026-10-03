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
	"path/filepath"
	"strings"
	"testing"

	"shfm/internal/drives"
)

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
		{kind: "local"},             // row 1 (title "Local" at 0)
		{kind: "local"},             // row 2
		{kind: "removable-mounted"}, // row 4 (blank at 3)
		{kind: "format-request"},    // row 5 (same group as removable-mounted)
		{kind: "mtp"},               // row 7 (blank at 6)
		{kind: "new-smb"},           // row 10 (blank at 8, title "Remote" at 9)
		{kind: "new-nfs"},           // row 11 (same group as new-smb)
		{kind: "cloud"},             // row 14 (blank at 12, title "Cloud" at 13)
		{kind: "new-cloud"},         // row 16 (blank at 15)
	}
	want := []int{1, 2, 4, 5, 7, 10, 11, 14, 16}
	got := sourceMenuRows(entries)
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row[%d] = %d, want %d (full: %v)", i, got[i], want[i], got)
		}
	}
	var titles []string
	for _, l := range sourceMenuLayout(entries) {
		if l.title != "" {
			titles = append(titles, l.title)
		}
	}
	if strings.Join(titles, ",") != "Local,Remote,Cloud" {
		t.Errorf("section titles = %v", titles)
	}
}

func TestSourceMenuRowsNoSeparatorForSingleGroup(t *testing.T) {
	entries := []sourceMenuEntry{{kind: "local"}, {kind: "local"}, {kind: "local"}}
	got := sourceMenuRows(entries)
	want := []int{1, 2, 3} // under the "Local" title
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestSelectLocalDriveOpensHome: choosing the local drive the home folder
// is on opens the home folder; any other drive, one whose mount point
// merely contains a drive with the home on it included, opens its root.
func TestSelectLocalDriveOpensHome(t *testing.T) {
	root := t.TempDir()
	homeDrive := filepath.Join(root, "home")
	home := filepath.Join(homeDrive, "user")
	other := t.TempDir()
	for _, d := range []string{home, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	local := func(mount string) sourceMenuEntry {
		return sourceMenuEntry{kind: "local", local: drives.LocalDrive{MountPoint: mount}}
	}

	for _, tc := range []struct {
		desc    string
		entries []sourceMenuEntry
		pick    int
		want    string
	}{
		{"home on the only drive", []sourceMenuEntry{local(root)}, 0, home},
		{"home on its own drive", []sourceMenuEntry{local(root), local(homeDrive)}, 1, home},
		{"the drive above the home's", []sourceMenuEntry{local(root), local(homeDrive)}, 0, root},
		{"another drive", []sourceMenuEntry{local(root), local(other)}, 1, other},
	} {
		m := newTestModel()
		m.sourceMenuEntries = tc.entries
		m.dialog = Dialog{Kind: DialogSourceMenu, ItemIdx: tc.pick}
		m.selectSourceMenuItem()
		if got := m.activePane().Path; got != tc.want {
			t.Errorf("%s: opened %s, want %s", tc.desc, got, tc.want)
		}
	}
}

// TestHomeOnDriveThroughSymlink: a home reached through a symlink is on
// the drive it resolves to.
func TestHomeOnDriveThroughSymlink(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "var", "home", "user")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "home")
	if err := os.Symlink(filepath.Join(root, "var", "home"), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(link, "user"))
	varDrive := filepath.Join(root, "var")
	entries := []sourceMenuEntry{
		{kind: "local", local: drives.LocalDrive{MountPoint: root}},
		{kind: "local", local: drives.LocalDrive{MountPoint: varDrive}},
	}
	if got, ok := homeOnDrive(varDrive, entries); !ok || got != real {
		t.Errorf("homeOnDrive(%s) = %q, %v; want %q", varDrive, got, ok, real)
	}
	if got, ok := homeOnDrive(root, entries); ok {
		t.Errorf("homeOnDrive(%s) = %q, want none: the home resolves onto %s", root, got, varDrive)
	}
}
