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
	"testing"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/archive"
	"shfm/internal/vfs"
)

// archiveTestModel returns a model whose active pane shows dir, with a
// synthetic HOME/XDG so nothing of the real user's is read or written.
func archiveTestModel(t *testing.T, dir string) *Model {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".data"))
	m := newTestModel()
	m.panes[0] = NewPane(vfs.NewLocalFS("Local", dir), dir, false, 0, nil)
	m.active = 0
	return m
}

func moveCursorTo(t *testing.T, p *Pane, name string) {
	t.Helper()
	for i, e := range p.Entries {
		if e.Name == name {
			p.Cursor = i
			return
		}
	}
	t.Fatalf("%s not listed", name)
}

// waitTask applies the task's messages until it finishes.
func waitTask(t *testing.T, m *Model) *Task {
	t.Helper()
	for {
		msg := <-m.taskCh
		m.handleTaskMsg(msg)
		if msg.finished {
			return m.taskByID(msg.id)
		}
	}
}

func TestCreateArchiveDialogThenExtract(t *testing.T) {
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, "docs", "a.txt"), []byte("alpha"), 0o644)
	m := archiveTestModel(t, dir)
	p := m.activePane()
	moveCursorTo(t, p, "docs")

	m.askCreateArchive()
	d := &m.dialog
	kinds := archive.Creatable()
	if d.Kind != DialogCreateArchive || len(d.Items) != len(kinds) {
		t.Fatalf("dialog %v with %d formats", d.Kind, len(d.Items))
	}
	if got, want := d.Inputs[0].Value(), "docs"+kinds[0].Ext(); got != want {
		t.Errorf("default name %q, want %q", got, want)
	}
	// Down picks the next format and swaps the extension.
	m.updateDialogKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if got, want := d.Inputs[0].Value(), "docs"+kinds[1].Ext(); got != want || d.ItemIdx != 1 {
		t.Errorf("after down: %q (#%d), want %q", got, d.ItemIdx, want)
	}
	// A name typed without extension gets the format's.
	d.Inputs[0].SetValue("bundle")
	m.confirmDialog()
	if m.dialog.Kind != DialogProgress {
		t.Fatalf("dialog %v, message %q", m.dialog.Kind, m.dialog.Message)
	}
	if task := waitTask(t, m); task.ErrorCount > 0 {
		t.Fatalf("create: %v", task.LastError)
	}
	name := "bundle" + kinds[1].Ext()

	// The same name again is refused, never overwritten.
	m.askCreateArchive()
	m.dialog.Inputs[0].SetValue(name)
	m.dialog.ItemIdx = 1
	m.confirmDialog()
	if m.dialog.Kind != DialogCreateArchive || m.dialog.Message == "" {
		t.Errorf("existing name accepted: %v %q", m.dialog.Kind, m.dialog.Message)
	}
	m.dialog = Dialog{}

	p.Load()
	moveCursorTo(t, p, name)
	m.doExtract()
	if task := waitTask(t, m); task.ErrorCount > 0 {
		t.Fatalf("extract: %v", task.LastError)
	}
	b, err := os.ReadFile(filepath.Join(dir, "bundle", "bundle", "docs", "a.txt"))
	if err != nil || string(b) != "alpha" {
		t.Errorf("extracted a.txt: %q, %v", b, err)
	}
	if m.archiveKind != kinds[1] {
		t.Errorf("format not remembered: %v", m.archiveKind)
	}
}

func TestExtractIgnoresNonArchives(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "plain.txt"), []byte("x"), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.activePane(), "plain.txt")
	m.doExtract()
	if m.dialog.Kind != DialogNone || len(m.tasks) != 0 {
		t.Errorf("extract started on a non-archive")
	}
}
