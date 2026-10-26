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

// propsTestModel shows dir in the active pane, cursor on name.
func propsTestModel(t *testing.T, fsys vfs.FileSystem, dir, name string) *Model {
	t.Helper()
	m := newTestModel()
	p := NewPane(fsys, dir, false, m.active, m.sizeCh)
	m.panes[m.active] = p
	moveCursorTo(t, p, name)
	return m
}

// runDone runs an elevatable command synchronously and applies its result.
func runDone(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatalf("no command (status %q)", m.status)
	}
	msg := cmd()
	if d, ok := msg.(elevatedDoneMsg); ok && d.err != "" {
		t.Fatalf("failed: %s", d.err)
	}
	m.Update(msg)
}

func ctrlR(m *Model) tea.Cmd {
	cmd, _ := m.updateDialogKey(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	return cmd
}

func permOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

// The first change made in Properties records the entry's permissions;
// later changes keep that original; Ctrl+R puts it back and forgets it.
func TestPropertiesRememberAndRestoreOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	m := propsTestModel(t, vfs.NewLocalFS("Local", dir), dir, "f")

	for _, mode := range []string{"600", "644"} {
		m.openProperties()
		if mode == "644" && !strings.Contains(m.renderDialogBox(), "Original:") {
			t.Errorf("after a change, Properties should show the original:\n%s", m.renderDialogBox())
		}
		m.dialog.Inputs[0].SetValue(mode)
		runDone(t, m, m.applyProperties())
	}
	if got := permOf(t, path); got != 0o644 {
		t.Fatalf("mode after edits = %o, want 644", got)
	}
	o, ok := m.originals().Get(permKey(m.activePane().FS, path))
	if !ok || o.Mode.Perm() != 0o640 || o.Owner == "" {
		t.Fatalf("original = %+v, %v; want the first mode 640 and the owner", o, ok)
	}

	m.openProperties()
	runDone(t, m, ctrlR(m))
	if got := permOf(t, path); got != 0o640 {
		t.Errorf("restored mode = %o, want 640", got)
	}
	if _, ok := m.originals().Get(permKey(m.activePane().FS, path)); ok {
		t.Error("original still recorded after restoring it")
	}
	m.openProperties()
	if strings.Contains(m.renderDialogBox(), "Original:") {
		t.Error("no original should be shown once restored")
	}
}

// The mode field shows and takes setuid/setgid/sticky, which a restore
// must not lose either.
func TestPropertiesModeKeepsSpecialBits(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "shared")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	m := propsTestModel(t, vfs.NewLocalFS("Local", root), root, "shared")
	m.openProperties()
	if got := m.dialog.Inputs[0].Value(); got != "2755" {
		t.Fatalf("mode field = %q, want 2755", got)
	}
	m.dialog.Inputs[0].SetValue("1777")
	runDone(t, m, m.applyProperties())
	if got := permOf(t, dir); got != 0o777|os.ModeSticky {
		t.Fatalf("mode = %v, want sticky 777", got)
	}
	m.openProperties()
	runDone(t, m, ctrlR(m))
	if got := permOf(t, dir); got != 0o755|os.ModeSetgid {
		t.Errorf("restored mode = %v, want setgid 755", got)
	}
}

// An entry hidden by a folder without x: u adds just the user's x bit to
// that folder, whose Properties can then put the original back.
func TestUnlockParentFolderAndRestore(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the missing x permission")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "config.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o640); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	fsys := vfs.NewLocalFS("Local", root)
	m := propsTestModel(t, fsys, locked, "config.yaml")
	m.openProperties()
	if len(m.dialog.Inputs) != 0 {
		t.Fatal("details unknown: Properties should be read-only")
	}
	if box := m.renderDialogBox(); !strings.Contains(box, "e read as root") || !strings.Contains(box, "u unlock folder") {
		t.Errorf("emergency actions not offered:\n%s", box)
	}
	cmd, _ := m.updateDialogKey(tea.KeyPressMsg{Code: 'u', Text: "u"})
	runDone(t, m, cmd)
	if got := permOf(t, locked); got != 0o740 {
		t.Fatalf("unlocked mode = %o, want 740 (only the owner's x added)", got)
	}
	if !strings.Contains(m.status, "640→740") {
		t.Errorf("status %q should tell the change", m.status)
	}
	if e := m.activePane().Entries[m.activePane().Cursor]; e.StatErr != nil {
		t.Errorf("after unlocking, details should be readable: %+v", e)
	}

	e, err := fsys.Stat(locked)
	if err != nil {
		t.Fatal(err)
	}
	m.dialog = propertiesDialog(fsys, locked, e)
	if !strings.Contains(m.renderDialogBox(), "640") {
		t.Errorf("folder's Properties should show its original 640:\n%s", m.renderDialogBox())
	}
	runDone(t, m, ctrlR(m))
	if got := permOf(t, locked); got != 0o640 {
		t.Errorf("restored mode = %o, want 640", got)
	}
}

// rootStatFS stands in for pkexec: its StatElevated returns canned details.
type rootStatFS struct {
	*vfs.LocalFS
	e vfs.Entry
}

func (f rootStatFS) StatElevated(string) (vfs.Entry, error) { return f.e, nil }

// e reads hidden details as root and makes the dialog editable with them.
func TestReadPropertiesAsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the missing x permission")
	}
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	os.Mkdir(locked, 0o755)
	os.WriteFile(filepath.Join(locked, "config.yaml"), []byte("x"), 0o644)
	if err := os.Chmod(locked, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	fsys := rootStatFS{vfs.NewLocalFS("Local", root),
		vfs.Entry{Name: "config.yaml", Mode: 0o600 | os.ModeSetuid, Size: 9, Owner: "root", Group: "wheel"}}
	m := propsTestModel(t, fsys, locked, "config.yaml")
	m.openProperties()
	cmd, _ := m.updateDialogKey(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("e should read the details as root")
	}
	m.Update(cmd())
	d := m.dialog
	if !d.PropsAsRoot || len(d.Inputs) != 3 {
		t.Fatalf("dialog should be editable with root's details: %+v", d)
	}
	if d.Inputs[0].Value() != "4600" || d.Inputs[1].Value() != "root" || d.Inputs[2].Value() != "wheel" {
		t.Errorf("fields = %q %q %q", d.Inputs[0].Value(), d.Inputs[1].Value(), d.Inputs[2].Value())
	}
	if !strings.Contains(m.renderDialogBox(), "read as root") {
		t.Error("dialog should say the details were read as root")
	}
}
