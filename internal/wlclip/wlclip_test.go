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

package wlclip

import (
	"bytes"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// These tests talk to a real compositor, so they only run against one
// named explicitly — never the desktop session's own clipboard:
//
//	SHFM_WLCLIP_TEST_DISPLAY=wayland-0 XDG_RUNTIME_DIR=/run/user/1000/shfmwl go test ./internal/wlclip/
//
// (a throwaway headless compositor). wl-copy/wl-paste, when installed,
// serve as an independent reference implementation.
func testClient(t *testing.T) *Client {
	t.Helper()
	d := os.Getenv("SHFM_WLCLIP_TEST_DISPLAY")
	if d == "" {
		t.Skip("set SHFM_WLCLIP_TEST_DISPLAY to a throwaway compositor's display to run")
	}
	t.Setenv("WAYLAND_DISPLAY", d)
	c, err := Connect()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func wlTool(t *testing.T, name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed", name)
	}
	return p
}

// waitFor polls cond for up to 3s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSetSelectionReadByWlPaste(t *testing.T) {
	c := testClient(t)
	uris := "file:///tmp/a%20b.txt\r\nfile:///tmp/c.txt\r\n"
	if err := c.SetSelection(map[string][]byte{
		"text/uri-list": []byte(uris),
		"text/plain":    []byte("/tmp/a b.txt\n/tmp/c.txt"),
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "our selection", func() bool { _, owned := c.Selection(); return owned })

	out, err := exec.Command(wlTool(t, "wl-paste"), "--no-newline", "--type", "text/uri-list").Output()
	if err != nil || string(out) != uris {
		t.Fatalf("wl-paste got %q, %v", out, err)
	}
	types, _ := exec.Command(wlTool(t, "wl-paste"), "--list-types").Output()
	if !strings.Contains(string(types), "text/uri-list") || !strings.Contains(string(types), "text/plain") {
		t.Fatalf("offered types: %q", types)
	}
	// Reading our own selection works too.
	got, err := c.Receive("text/plain", time.Second)
	if err != nil || string(got) != "/tmp/a b.txt\n/tmp/c.txt" {
		t.Fatalf("Receive(own) = %q, %v", got, err)
	}
}

func TestReadSelectionFromWlCopy(t *testing.T) {
	c := testClient(t)
	changed := make(chan struct{}, 8)
	c.OnChange(func() { changed <- struct{}{} })

	cmd := exec.Command(wlTool(t, "wl-copy"), "--type", "text/uri-list")
	cmd.Stdin = bytes.NewBufferString("file:///srv/x.iso\r\n")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command(wlTool(t, "wl-copy"), "--clear").Run() })
	waitFor(t, "external selection", func() bool {
		mimes, owned := c.Selection()
		return !owned && slices.Contains(mimes, "text/uri-list")
	})
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("OnChange not called")
	}
	got, err := c.Receive("text/uri-list", 2*time.Second)
	if err != nil || string(got) != "file:///srv/x.iso\r\n" {
		t.Fatalf("Receive = %q, %v", got, err)
	}
}

func TestLosingSelection(t *testing.T) {
	c := testClient(t)
	c.SetSelection(map[string][]byte{"text/plain": []byte("mine")})
	waitFor(t, "our selection", func() bool { _, owned := c.Selection(); return owned })

	cmd := exec.Command(wlTool(t, "wl-copy"))
	cmd.Stdin = bytes.NewBufferString("theirs")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command(wlTool(t, "wl-copy"), "--clear").Run() })
	waitFor(t, "losing the selection", func() bool { _, owned := c.Selection(); return !owned })
	got, err := c.Receive("text/plain", 2*time.Second)
	if err != nil || string(got) != "theirs" {
		t.Fatalf("Receive = %q, %v", got, err)
	}
}

func TestConnectWithoutWayland(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	if _, err := Connect(); err == nil {
		t.Fatal("expected ErrUnavailable")
	}
}
