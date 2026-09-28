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
	"net"
	"os"
	"strings"
	"testing"

	"github.com/vmware/go-nfs-client/nfs/rpc"
)

func TestRPCReservedPortUnprivileged(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := rpc.DialTCP("tcp", nil, ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.ReservedPort() {
		t.Fatal("an ephemeral source port was reported as reserved")
	}
}

func TestReservedPortHintNamesBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	h := reservedPortHint()
	if !strings.Contains(h, "cap_net_bind_service=+ep "+exe) || !strings.Contains(h, "insecure") {
		t.Fatalf("hint %q lacks the setcap command for %s or the insecure alternative", h, exe)
	}
}
