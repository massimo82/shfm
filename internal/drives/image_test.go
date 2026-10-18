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

package drives

import (
	"bytes"
	"testing"
)

// testImage is a pattern of n bytes, n chosen by the tests to span
// several chunks and end off any block boundary.
func testImage(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i>>9)
	}
	return b
}

func TestIsImageName(t *testing.T) {
	for name, want := range map[string]bool{
		"debian.iso": true, "RPI.IMG": true, "a.iso": true,
		".iso": false, "debian.iso.xz": false, "notes.txt": false, "iso": false,
	} {
		if got := IsImageName(name); got != want {
			t.Errorf("IsImageName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestISONotHybrid(t *testing.T) {
	hybrid := make([]byte, 2048)
	hybrid[510], hybrid[511] = 0x55, 0xAA
	plain := make([]byte, 2048)
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"linux.iso", hybrid, false},
		{"windows.ISO", plain, true},
		{"raspios.img", plain, false}, // not an ISO: never reported
		{"tiny.iso", plain[:100], false},
	}
	for _, c := range cases {
		if got := ISONotHybrid(c.name, bytes.NewReader(c.data)); got != c.want {
			t.Errorf("ISONotHybrid(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
