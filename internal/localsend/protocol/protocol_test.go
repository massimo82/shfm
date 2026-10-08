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

package protocol

import (
	"net/http"
	"testing"
)

func TestPeerURL(t *testing.T) {
	cases := map[string]string{
		"192.168.1.5":  "https://192.168.1.5:53317",
		"fd00::5":      "https://[fd00::5]:53317",
		"fe80::1%eth0": "https://[fe80::1%25eth0]:53317",
	}
	for ip, want := range cases {
		p := Peer{Info: Info{Port: 53317, HTTPS: true}, IP: ip}
		if got := p.URL(); got != want {
			t.Errorf("URL(%q) = %q, want %q", ip, got, want)
		}
		req, err := http.NewRequest("GET", p.URL()+"/x", nil)
		if err != nil {
			t.Errorf("%q: %v", ip, err)
		} else if req.URL.Hostname() != ip {
			t.Errorf("%q: host %q", ip, req.URL.Hostname())
		}
	}
}

func TestRequestPeer(t *testing.T) {
	for remote, want := range map[string]string{
		"192.168.1.5:4000":          "192.168.1.5",
		"[::ffff:192.168.1.5]:4000": "192.168.1.5",
		"[fe80::1%eth0]:4000":       "fe80::1%eth0",
	} {
		if ip, _ := RequestPeer(&http.Request{RemoteAddr: remote}); ip != want {
			t.Errorf("RequestPeer(%q) = %q, want %q", remote, ip, want)
		}
	}
}
