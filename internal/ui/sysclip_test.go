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
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// End-to-end test of the shared clipboard against a throwaway compositor
// (never the desktop session's clipboard): see internal/wlclip's tests
// for how to run one; needs wl-clipboard as the "other application".
func TestSharedClipboardBothWays(t *testing.T) {
	d := os.Getenv("SHFM_WLCLIP_TEST_DISPLAY")
	if d == "" {
		t.Skip("set SHFM_WLCLIP_TEST_DISPLAY to a throwaway compositor's display to run")
	}
	for _, tool := range []string{"wl-copy", "wl-paste"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	t.Setenv("WAYLAND_DISPLAY", d)
	m, tmp := mirrorTestModel(t)
	m.handleSysclipReady(connectSysclip().(sysclipReadyMsg))
	if m.sysclip == nil {
		t.Fatal("no system clipboard")
	}
	t.Cleanup(func() { m.sysclip.Close() })

	// shfm → other apps: Ctrl+C on src/data.
	m.panes[1].Load()
	m.active = 1
	m.panes[1].Selected = map[string]bool{"data": true}
	m.doCopyToClipboard()
	want := "file://" + filepath.Join(tmp, "src", "data") + "\r\n"
	var got []byte
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		got, _ = exec.Command("wl-paste", "--no-newline", "--type", "text/uri-list").Output()
		if string(got) == want {
			break
		}
	}
	if string(got) != want {
		t.Fatalf("wl-paste text/uri-list = %q, want %q", got, want)
	}
	if plain, _ := exec.Command("wl-paste", "--no-newline").Output(); string(plain) != filepath.Join(tmp, "src", "data") {
		t.Fatalf("wl-paste text = %q", plain)
	}

	// Other apps → shfm: a file "copied" elsewhere, pasted with Ctrl+V.
	ext := filepath.Join(tmp, "elsewhere", "report 2026.txt")
	os.MkdirAll(filepath.Dir(ext), 0o755)
	os.WriteFile(ext, []byte("from another app"), 0o644)
	cmd := exec.Command("wl-copy", "--type", "text/uri-list")
	cmd.Stdin = bytes.NewBufferString(fileURI(m.panes[0].FS, ext) + "\r\n")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("wl-copy", "--clear").Run() })
	select {
	case msg := <-m.sysclipCh:
		m.handleSysclipFiles(msg)
	case <-time.After(3 * time.Second):
		t.Fatal("shfm didn't see the external copy")
	}
	if m.clipboardCount() != 1 || !strings.Contains(m.status, "system clipboard") {
		t.Fatalf("count=%d status=%q", m.clipboardCount(), m.status)
	}
	m.active = 0 // dst
	m.doPaste(true)
	waitTasks(t, m)
	if b, err := os.ReadFile(filepath.Join(tmp, "dst", "report 2026.txt")); err != nil || string(b) != "from another app" {
		t.Fatalf("pasted file: %q, %v", b, err)
	}

	// Copying in shfm again makes its own clipboard the current one.
	m.active = 1
	m.doCopyToClipboard()
	if m.useExtClip {
		t.Fatal("shfm's own copy should replace the external clipboard")
	}
}
