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
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"testing"

	"shfm/internal/config"
	"shfm/internal/drives"
	"shfm/internal/vfs"
)

func newTestModel() *Model {
	return New(config.Default(), config.DefaultKeyMap(), Start{})
}

func TestFormatDialogFlow(t *testing.T) {
	m := newTestModel()

	dev := drives.RemovableDevice{Path: "/dev/sdz1", Name: "sdz1", SizeBytes: 16 << 30}
	m.openFormatChoose(dev)

	if m.dialog.Kind != DialogFormatChoose {
		t.Fatalf("expected DialogFormatChoose, got %v", m.dialog.Kind)
	}
	if len(m.dialog.Items) != len(drives.FormatChoices) {
		t.Fatalf("expected %d filesystem choices, got %d", len(drives.FormatChoices), len(m.dialog.Items))
	}

	// Pick the second choice (FAT32) and confirm -> should move to Confirm1
	// with a RED warning message and the chosen filesystem carried over.
	m.dialog.ItemIdx = 1
	m.confirmDialog()
	if m.dialog.Kind != DialogFormatConfirm1 {
		t.Fatalf("expected DialogFormatConfirm1, got %v", m.dialog.Kind)
	}
	if m.dialog.FormatFSType != drives.FormatChoices[1].Type {
		t.Fatalf("FormatFSType = %v, want %v", m.dialog.FormatFSType, drives.FormatChoices[1].Type)
	}
	if m.dialog.FormatDevice.Path != dev.Path {
		t.Fatalf("FormatDevice.Path = %q, want %q", m.dialog.FormatDevice.Path, dev.Path)
	}

	// Confirming step 1 -> DialogFormatConfirm2, requiring a typed "YES".
	m.confirmDialog()
	if m.dialog.Kind != DialogFormatConfirm2 {
		t.Fatalf("expected DialogFormatConfirm2, got %v", m.dialog.Kind)
	}
	if len(m.dialog.Inputs) != 1 {
		t.Fatalf("expected exactly one text input for the YES confirmation, got %d", len(m.dialog.Inputs))
	}

	// Wrong / incomplete text must NOT proceed: the dialog stays open so
	// the destructive action can't be triggered by a stray Enter press.
	m.dialog.Inputs[0].SetValue("yes") // wrong case
	m.confirmDialog()
	if m.dialog.Kind != DialogFormatConfirm2 {
		t.Fatalf("wrong confirmation text should NOT proceed past DialogFormatConfirm2, got %v", m.dialog.Kind)
	}

	m.dialog.Inputs[0].SetValue("Y")
	m.confirmDialog()
	if m.dialog.Kind != DialogFormatConfirm2 {
		t.Fatalf("incomplete confirmation text should NOT proceed past DialogFormatConfirm2, got %v", m.dialog.Kind)
	}

	// The exact literal "YES" proceeds: a background Task is started and a
	// progress dialog opens immediately.
	m.dialog.Inputs[0].SetValue("YES")
	m.confirmDialog()
	if m.dialog.Kind != DialogProgress {
		t.Fatalf("expected DialogProgress after typing YES, got %v", m.dialog.Kind)
	}
	if len(m.tasks) != 1 {
		t.Fatalf("expected exactly one background task to have been started, got %d", len(m.tasks))
	}
	if m.tasks[0].Kind != TaskFormat {
		t.Fatalf("task kind = %v, want TaskFormat", m.tasks[0].Kind)
	}

	// Drain the task's message(s) until completion (it will fail since
	// there's no real device / no udisks2 available in this environment —
	// that's expected and fine: we're only verifying the UI state
	// machine, not that formatting a nonexistent device actually
	// succeeds; on error, startSimpleTask sends an item-error message
	// followed by a finished message).
	for {
		msg := <-m.taskCh
		m.handleTaskMsg(msg)
		if msg.finished {
			break
		}
	}
	if !m.tasks[0].Finished {
		t.Fatalf("expected the task to be marked finished after draining its completion message")
	}
}

func TestFormatMenuHidesSystemDisk(t *testing.T) {
	sysDisk, err := drives.SystemDiskName()
	if err != nil {
		t.Skip("cannot determine system disk in this environment")
	}
	if !drives.IsSystemDisk("/dev/" + sysDisk) {
		t.Errorf("IsSystemDisk should report true for the system disk %q", sysDisk)
	}
	if drives.IsSystemDisk("/dev/sdz1") {
		t.Errorf("IsSystemDisk should report false for an unrelated device")
	}
}

// A "lost+found" folder away from a filesystem's root is an ordinary
// folder: only the fsck one at a mount point is hidden.
func TestLostAndFoundShownOutsideMountRoot(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(sub, "lost+found"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs := vfs.NewLocalFS(dir, dir)
	p := NewPane(fs, sub, false, 0, nil)
	p.Load()
	found := false
	for _, e := range p.Entries {
		if e.Name == "lost+found" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lost+found outside a mount root should be listed, got %v", p.Entries)
	}
}

// Toggling hidden files updates both panes, keeps the cursor on its entry
// and remembers the choice in the config.
func TestToggleHidden(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	for _, name := range []string{".dot", "b"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := newTestModel()
	for i := range m.panes {
		fs := vfs.NewLocalFS(dir, dir)
		m.replaceFS(i, fs, dir)
		m.panes[i].Load()
	}
	names := func(p *Pane) []string {
		var out []string
		for _, e := range p.Entries {
			if !IsParentEntry(e) {
				out = append(out, e.Name)
			}
		}
		return out
	}
	if got := names(m.panes[0]); len(got) != 1 || got[0] != "b" {
		t.Fatalf("hidden files should start hidden, got %v", got)
	}
	for _, p := range m.panes {
		p.Cursor = len(p.Entries) - 1 // on "b"
	}

	m.toggleHidden(10)
	if !m.cfg.ShowHidden {
		t.Fatal("config ShowHidden should be true after toggling")
	}
	for i, p := range m.panes {
		if len(names(p)) != 2 {
			t.Fatalf("pane %d should list the dotfile too, got %v", i, names(p))
		}
		if e, _ := p.CurrentEntry(); e.Name != "b" {
			t.Fatalf("pane %d cursor should stay on b, is on %q", i, e.Name)
		}
	}

	m.toggleHidden(10)
	if got := names(m.panes[1]); len(got) != 1 || got[0] != "b" {
		t.Fatalf("hidden files should be hidden again, got %v", got)
	}
}

// Opening the fsck lost+found at a mount point says what it is, not just
// "permission denied". Needs one on this machine (the root filesystem's,
// on ext4) that the user running the tests can't read.
func TestLostAndFoundPermissionMessage(t *testing.T) {
	if _, err := os.ReadDir("/lost+found"); err == nil || !errors.Is(err, iofs.ErrPermission) {
		t.Skip("no unreadable /lost+found here")
	}
	fs := vfs.NewLocalFS("Local", "/")
	p := NewPane(fs, "/lost+found", true, 0, nil)
	if p.Err != errLostAndFound {
		t.Fatalf("error = %v, want errLostAndFound", p.Err)
	}
}
