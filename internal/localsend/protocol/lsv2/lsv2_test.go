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

package lsv2

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"shfm/internal/localsend/protocol"
)

func TestCheckedBody(t *testing.T) {
	sum := sha256.Sum256([]byte("hello"))
	good := hex.EncodeToString(sum[:])
	cases := []struct {
		body string
		size int64
		sha  string
		want error
	}{
		{"hello", 5, good, nil},
		{"hello", 5, "", nil},
		{"hellO", 5, good, protocol.ErrChecksum},
		{"hell", 5, "", errSize},
		{"hello!", 5, "", errSize},
		{"", 0, "", nil},
	}
	for _, c := range cases {
		b := &checkedBody{r: strings.NewReader(c.body), size: c.size}
		if c.sha != "" {
			b.hash, b.want = sha256.New(), c.sha
		}
		_, err := io.ReadAll(b)
		if !errors.Is(err, c.want) {
			t.Errorf("%q (size %d): err = %v, want %v", c.body, c.size, err, c.want)
		}
	}
}

func TestAnnouncement(t *testing.T) {
	in := protocol.Announcement{Info: protocol.Info{Alias: "a", Fingerprint: "F", Port: 53317, HTTPS: true, DeviceType: "fridge"}, Announce: true}
	out, err := DecodeAnnouncement(EncodeAnnouncement(in))
	if err != nil {
		t.Fatal(err)
	}
	if out.Alias != "a" || !out.Announce || !out.HTTPS || out.Version != Version || out.DeviceType != protocol.TypeDesktop {
		t.Fatalf("decoded %+v", out)
	}
	if _, err := DecodeAnnouncement([]byte(`{"alias":"x"}`)); err == nil {
		t.Fatal("incomplete announcement accepted")
	}
}
