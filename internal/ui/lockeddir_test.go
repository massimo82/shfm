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
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/vfs"
)

// unlistable makes root/locked holding a file, then takes all of locked's
// permissions away: the user can't list or enter it.
func unlistable(t *testing.T) (root, locked string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root lists any folder")
	}
	root = t.TempDir()
	locked = filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "secret.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	return root, locked
}

func paneKey(m *Model, k string) tea.Cmd {
	var msg tea.KeyPressMsg
	if k == "esc" {
		msg = tea.KeyPressMsg{Code: tea.KeyEscape}
	} else {
		msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
	}
	_, cmd := m.handleKey(msg)
	return cmd
}

// enterLocked shows root in the active pane, then enters locked.
func enterLocked(t *testing.T, fsys vfs.FileSystem, root string) *Model {
	t.Helper()
	m := propsTestModel(t, fsys, root, "locked")
	m.enterOrOpen()
	if p := m.activePane(); p.Err == nil {
		t.Fatalf("entering locked should fail: %+v", p.Entries)
	}
	return m
}

// The error of a folder that can't be listed says how to get in and back.
func TestLockedFolderShowsWaysInAndBack(t *testing.T) {
	root, _ := unlistable(t)
	m := enterLocked(t, vfs.NewLocalFS("Local", root), root)
	m.width, m.height = 120, 30
	view := m.renderPane(m.active, 100, 20)
	for _, want := range []string{"u    unlock it: 000→500", "e    list it as root", "Esc  back to " + root} {
		if !strings.Contains(view, want) {
			t.Errorf("pane should show %q:\n%s", want, view)
		}
	}
}

// Esc goes back to the folder the user came from, even one that isn't the
// parent, the cursor on the locked folder when it is.
func TestEscGoesBackFromLockedFolder(t *testing.T) {
	root, locked := unlistable(t)
	m := enterLocked(t, vfs.NewLocalFS("Local", root), root)
	paneKey(m, "esc")
	p := m.activePane()
	if p.Path != root || p.Err != nil {
		t.Fatalf("Esc should go back to %s, at %s (%v)", root, p.Path, p.Err)
	}
	if e, _ := p.CurrentEntry(); e.Name != "locked" {
		t.Errorf("cursor on %q, want locked", e.Name)
	}

	elsewhere := t.TempDir()
	p.Path = elsewhere
	p.Load()
	p.Path = locked // as typed in the PATH field
	p.Load()
	paneKey(m, "esc")
	if p.Path != elsewhere {
		t.Errorf("Esc should go back to %s, at %s", elsewhere, p.Path)
	}
}

// u gives the user r and x on the folder, recording its mode: the folder
// lists, and Ctrl+R in its Properties restores it.
func TestUnlockLockedFolder(t *testing.T) {
	root, locked := unlistable(t)
	m := enterLocked(t, vfs.NewLocalFS("Local", root), root)
	runDone(t, m, paneKey(m, "u"))
	p := m.activePane()
	if got := permOf(t, locked); got != 0o500 {
		t.Errorf("unlocked mode = %o, want 500", got)
	}
	if p.Err != nil || len(p.Entries) != 2 { // ".." and secret.txt
		t.Fatalf("unlocked folder should list: %v %+v", p.Err, p.Entries)
	}

	p.GoUp()
	moveCursorTo(t, p, "locked")
	m.openProperties()
	runDone(t, m, ctrlR(m))
	if got := permOf(t, locked); got != 0 {
		t.Errorf("restored mode = %o, want 0", got)
	}
}

// rootListFS stands in for pkexec: its ListElevated returns canned entries.
type rootListFS struct {
	*vfs.LocalFS
	entries []vfs.Entry
	calls   *int
}

func (f *rootListFS) ListElevated(string) ([]vfs.Entry, error) {
	*f.calls++
	return f.entries, nil
}

// e lists the folder as root, unchanged, marked as such; the listing stays
// while the pane is inside it, R reads it again, leaving it drops it.
func TestListLockedFolderAsRoot(t *testing.T) {
	root, locked := unlistable(t)
	calls := 0
	fsys := &rootListFS{vfs.NewLocalFS("Local", root), []vfs.Entry{
		{Name: "secret.txt", Mode: 0o600, Size: 1, Owner: "root", Group: "root"},
		{Name: "sub", IsDir: true, Mode: os.ModeDir | 0o700, Owner: "root", Group: "root"},
	}, &calls}
	m := enterLocked(t, fsys, root)
	m.Update(paneKey(m, "e")())
	p := m.activePane()
	if p.Err != nil || len(p.Entries) != 3 {
		t.Fatalf("root listing not shown: %v %+v", p.Err, p.Entries)
	}
	if got := permOf(t, locked); got != 0 {
		t.Errorf("listing as root changed the folder to %o", got)
	}
	m.width, m.height = 120, 30
	if !strings.Contains(m.renderPane(m.active, 100, 20), "[as root]") {
		t.Error("the PATH row should say the listing is root's")
	}

	moveCursorTo(t, p, "sub")
	m.enterOrOpen() // can't be entered either
	paneKey(m, "esc")
	if p.Path != locked || len(p.Entries) != 3 || calls != 1 {
		t.Fatalf("back from sub should show the root listing again without asking: %s %+v calls %d", p.Path, p.Entries, calls)
	}
	if cmd := func() tea.Cmd { _, c := m.handleKey(tea.KeyPressMsg{Code: 'R', Text: "R"}); return c }(); cmd == nil {
		t.Fatal("R should read the folder again as root")
	} else {
		m.Update(cmd())
	}
	if calls != 2 {
		t.Errorf("R should have listed as root again (calls %d)", calls)
	}

	p.GoUp()
	p.Path = locked
	p.Load()
	if p.Err == nil {
		t.Error("after leaving the folder its root listing should be dropped")
	}
}
