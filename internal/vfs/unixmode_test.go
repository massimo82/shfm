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
	"os"
	"testing"
)

func TestUnixModeRoundTrip(t *testing.T) {
	for _, raw := range []uint32{0o644, 0o2755, 0o4711, 0o1777, 0o7000} {
		if got := UnixMode(FromUnixMode(raw)); got != raw {
			t.Errorf("%o -> %o", raw, got)
		}
	}
	if m := FromUnixMode(0o042755); !m.IsDir() || m&os.ModeSetgid == 0 || m.Perm() != 0o755 {
		t.Errorf("st_mode 042755 = %v", m)
	}
	if m := FromUnixMode(0o120777); m&os.ModeSymlink == 0 {
		t.Errorf("st_mode 120777 = %v", m)
	}
	if got := chmodArg(0o755); got != "00755" {
		t.Errorf("chmodArg(755) = %q: setgid on a folder must be cleared explicitly", got)
	}
	if got := chmodArg(os.ModeSetgid | 0o770); got != "02770" {
		t.Errorf("chmodArg(setgid 770) = %q", got)
	}
}

func TestParseStatLine(t *testing.T) {
	e, err := parseStatLine("config.yaml", "81a4 0 0 1234 1700000000\n")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "config.yaml" || e.IsDir || e.Mode != 0o644 || e.Size != 1234 ||
		e.ModTime.Unix() != 1700000000 || e.Owner == "" || e.Group == "" {
		t.Errorf("parsed %+v", e)
	}
	if d, _ := parseStatLine("tls", "41ed 0 0 4096 1"); !d.IsDir {
		t.Errorf("41ed should be a folder: %+v", d)
	}
	for _, bad := range []string{"", "81a4 0 0", "zz 0 0 1 1"} {
		if _, err := parseStatLine("x", bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
}
