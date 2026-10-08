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

package localsend

import (
	"reflect"
	"strings"
	"testing"
)

func TestSafePath(t *testing.T) {
	cases := map[string][]string{
		"a.txt":             {"a.txt"},
		"Photos/2024/b.jpg": {"Photos", "2024", "b.jpg"},
		"../../etc/passwd":  {"etc", "passwd"},
		"/abs/x":            {"abs", "x"},
		`win\dir\f.txt`:     {"win", "dir", "f.txt"},
		"a/./b//c":          {"a", "b", "c"},
		"":                  {"untitled"},
		"..":                {"untitled"},
		"evil\x1b[2J\n.txt": {"evil_[2J_.txt"},
		"   /x":             {"x"},
	}
	for in, want := range cases {
		if got := safePath(in); !reflect.DeepEqual(got, want) {
			t.Errorf("safePath(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 200) + ".jpeg"
	got := safePath(long)[0]
	if len(got) > maxNameBytes || !strings.HasSuffix(got, ".jpeg") {
		t.Errorf("long name: %d bytes, %q", len(got), got[len(got)-10:])
	}
}

func TestNumbered(t *testing.T) {
	for in, want := range map[string]string{"a.txt": "a (2).txt", "noext": "noext (2)", ".hidden": ".hidden (2)"} {
		if got := numbered(in, 2); got != want {
			t.Errorf("numbered(%q) = %q, want %q", in, got, want)
		}
	}
}
