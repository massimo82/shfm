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

package filemanager1

import (
	"reflect"
	"testing"
)

func TestLocalPath(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"file:///home/u/Downloads/a%20b.pdf", "/home/u/Downloads/a b.pdf", true},
		{"file://localhost/tmp/x", "/tmp/x", true},
		{"/tmp/y/../z", "/tmp/z", true},
		{"file://otherhost/tmp/x", "", false},
		{"smb://nas/share/x", "", false},
		{"relative/path", "", false},
	} {
		got, ok := LocalPath(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("LocalPath(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestItemsGroupsByFolder(t *testing.T) {
	got, err := Items([]string{
		"file:///d1/a", "file:///d2/b", "file:///d1/c", "smb://nas/x",
	})
	want := [][]string{
		{"--select", "--", "/d1/a", "/d1/c"},
		{"--select", "--", "/d2/b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Items = %q, want %q", got, want)
	}
	if err == nil {
		t.Fatal("the smb:// URI should be reported")
	}
}

func TestFoldersAndProperties(t *testing.T) {
	folders, err := Folders([]string{"file:///d1", "file:///d2"})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"--", "/d1"}, {"--", "/d2"}}; !reflect.DeepEqual(folders, want) {
		t.Fatalf("Folders = %q, want %q", folders, want)
	}
	props, err := Properties([]string{"file:///d1/a"})
	if err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"--properties", "--", "/d1/a"}}; !reflect.DeepEqual(props, want) {
		t.Fatalf("Properties = %q, want %q", props, want)
	}
}
