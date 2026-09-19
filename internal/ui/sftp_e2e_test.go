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
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSFTPConnectEndToEnd exercises the exact same doConnectSFTP business
// logic the UI calls after the user fills in the connect form, against a
// real local SSH server (see the accompanying shell setup in this
// session), skipping only the tedious pty key-navigation to reach the
// "New SFTP connection..." menu entry.
func TestSFTPConnectEndToEnd(t *testing.T) {
	host := os.Getenv("SHFM_TEST_SFTP_HOST")
	if host == "" {
		t.Skip("SHFM_TEST_SFTP_HOST not set: skipping (requires a real local SSH server)")
	}
	port := os.Getenv("SHFM_TEST_SFTP_PORT")
	user := os.Getenv("SHFM_TEST_SFTP_USER")
	pass := os.Getenv("SHFM_TEST_SFTP_PASS")

	m := newTestModel()
	m.dialog = newConnectDialog(DialogConnectSFTP)
	m.dialog.Inputs[0].SetValue(host)
	m.dialog.Inputs[1].SetValue(port)
	m.dialog.Inputs[2].SetValue(user)
	m.dialog.Inputs[3].SetValue(pass)
	m.dialog.Inputs[4].SetValue("")

	cmd, _ := m.updateDialogKey(teaEnterMsg())
	_ = cmd

	// The connection now happens asynchronously (see connect.go) so the UI
	// never freezes on slow/misbehaving USB or network I/O — right after
	// Enter, the dialog is a "Connecting…" placeholder, not yet closed.
	if m.dialog.Kind != DialogConnecting {
		t.Fatalf("expected DialogConnecting right after submitting the form, got kind=%v message=%q", m.dialog.Kind, m.dialog.Message)
	}
	select {
	case res := <-m.connectCh:
		m.handleConnectResult(res)
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for the asynchronous SFTP connection to complete")
	}

	if m.dialog.Kind != DialogNone {
		t.Fatalf("expected the dialog to close on a successful connection, got kind=%v message=%q", m.dialog.Kind, m.dialog.Message)
	}
	p := m.activePane()
	if p.FS.Kind().String() != "sftp" {
		t.Fatalf("active pane's FS kind = %q, want sftp", p.FS.Kind().String())
	}

	found := false
	for _, e := range p.Entries {
		if e.Name == "remote.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected to see remote.txt in the listing, got %v", entryNames(p))
	}

	// The source should have been remembered for next time (without a
	// plaintext password on disk).
	if len(m.cfg.RemoteSources) != 1 {
		t.Fatalf("expected 1 saved remote source, got %d", len(m.cfg.RemoteSources))
	}
	saved := m.cfg.RemoteSources[0]
	if saved.Kind != "sftp" || saved.Host != host || saved.User != user {
		t.Errorf("saved source = %+v", saved)
	}
	if saved.EncryptedPassword == "" {
		t.Error("expected the password to have been saved encrypted")
	}
	if saved.EncryptedPassword == pass {
		t.Error("the saved password must NOT be stored in plain text")
	}
	decrypted, err := saved.DecryptedPassword()
	if err != nil || decrypted != pass {
		t.Errorf("DecryptedPassword() = %q, %v; want %q, nil", decrypted, err, pass)
	}
}

func entryNames(p *Pane) []string {
	names := make([]string, len(p.Entries))
	for i, e := range p.Entries {
		names[i] = e.Name
	}
	return names
}

func teaEnterMsg() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyEnter}
}
