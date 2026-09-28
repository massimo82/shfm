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
	"slices"
	"strings"
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
	kinds := archive.CreateKinds()
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

// withArchiveNeeds simulates a machine where only the formats in have can
// be read and written: every other one needs "lzip or bsdtar".
func withArchiveNeeds(t *testing.T, have ...archive.Kind) {
	t.Helper()
	oldRead, oldCreate := archiveReadNeeds, archiveCreateNeeds
	t.Cleanup(func() { archiveReadNeeds, archiveCreateNeeds = oldRead, oldCreate })
	needs := func(k archive.Kind) string {
		if slices.Contains(have, k) {
			return ""
		}
		return "lzip or bsdtar"
	}
	archiveReadNeeds, archiveCreateNeeds = needs, needs
}

func TestCreateArchiveDialogMissingTools(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha"), 0o644)
	withArchiveNeeds(t, archive.KindZip, archive.KindTar)
	m := archiveTestModel(t, dir)
	m.archiveKind = archive.KindTarLzip // remembered, but not writable here
	moveCursorTo(t, m.activePane(), "a.txt")

	m.askCreateArchive()
	d := &m.dialog
	kinds := archive.CreateKinds()
	zip, tar := slices.Index(kinds, archive.KindZip), slices.Index(kinds, archive.KindTar)
	if len(d.Items) != len(kinds) || d.ItemIdx != zip {
		t.Fatalf("%d formats, #%d selected; want all %d, ZIP selected", len(d.Items), d.ItemIdx, len(kinds))
	}
	if view := m.renderDialogBox(); !strings.Contains(view, ".tar.lz — lzip · needs lzip or bsdtar") {
		t.Errorf("disabled format not shown with its need:\n%s", view)
	}
	// The arrows skip what can't be written, both ways.
	m.updateDialogKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if d.ItemIdx != tar || d.Inputs[0].Value() != "a.tar" {
		t.Errorf("after down: #%d %q, want TAR", d.ItemIdx, d.Inputs[0].Value())
	}
	m.updateDialogKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if d.ItemIdx != zip {
		t.Errorf("after up: #%d, want ZIP", d.ItemIdx)
	}
	// Clicking a disabled one says what to install, and changes nothing.
	m.selectArchiveKind(slices.Index(kinds, archive.KindTarLzip))
	if d.ItemIdx != zip || d.Inputs[0].Value() != "a.zip" || d.Message != "Install lzip or bsdtar to create .tar.lz archives" {
		t.Errorf("disabled pick: #%d %q, message %q", d.ItemIdx, d.Inputs[0].Value(), d.Message)
	}
}

func TestExtractMissingTool(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.7z"), []byte("not read"), 0o644)
	withArchiveNeeds(t, archive.KindZip)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.activePane(), "a.7z")
	m.doExtract()
	if len(m.tasks) != 0 || !m.statusErr || m.status != "Can't extract a.7z: install lzip or bsdtar" {
		t.Errorf("tasks %d, status %q", len(m.tasks), m.status)
	}
	if _, err := os.Stat(filepath.Join(dir, "a")); err == nil {
		t.Errorf("destination folder created")
	}
}
