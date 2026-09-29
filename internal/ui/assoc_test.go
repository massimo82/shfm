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

// assocTestEnv sandboxes every XDG directory (see opener's tests), with
// a small MIME database and three applications: two declaring PDF, one
// not. It returns the user's mimeapps.list and a folder with a report.pdf.
func assocTestEnv(t *testing.T) (mimeapps, dir string) {
	t.Helper()
	configHome, dataHome, sysData := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_CONFIG_DIRS", t.TempDir())
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_DATA_DIRS", sysData)
	t.Setenv("XDG_CURRENT_DESKTOP", "")
	write := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(sysData, "mime", "globs2"), "50:application/pdf:*.pdf\n50:text/markdown:*.md\n50:image/png:*.png\n")
	write(filepath.Join(sysData, "mime", "magic"), "MIME-Magic\x00\n[50:application/pdf]\n>0=\x00\x04%PDF\n")
	apps := filepath.Join(sysData, "applications")
	write(filepath.Join(apps, "okular.desktop"), "[Desktop Entry]\nType=Application\nName=Okular\nExec=true %U\nMimeType=application/pdf;image/png;\n")
	write(filepath.Join(apps, "evince.desktop"), "[Desktop Entry]\nType=Application\nName=Evince\nExec=true %U\nMimeType=application/pdf;\n")
	write(filepath.Join(apps, "editor.desktop"), "[Desktop Entry]\nType=Application\nName=Editor\nExec=true %f\n")
	dir = t.TempDir()
	write(filepath.Join(dir, "report.pdf"), "%PDF")
	write(filepath.Join(dir, "scan"), "%PDF-1.7 no extension")
	write(filepath.Join(dir, "notes"), "plain text, no extension\n")
	return filepath.Join(configHome, "mimeapps.list"), dir
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "delete":
			msg = tea.KeyPressMsg{Code: tea.KeyDelete}
		default:
			for _, r := range k {
				m.updateDialogKey(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			continue
		}
		m.updateDialogKey(msg)
	}
}

func assocModel(t *testing.T, dir string) *Model {
	m := newTestModel()
	m.height = 40
	m.panes[m.active] = NewPane(vfs.NewLocalFS("Local", dir), dir, false, m.active, m.sizeCh)
	m.panes[m.active].Load()
	for i, e := range m.panes[m.active].Entries {
		if e.Name == "report.pdf" {
			m.panes[m.active].Cursor = i
		}
	}
	return m
}

func TestAssociationsDialogChangeAndReset(t *testing.T) {
	mimeapps, dir := assocTestEnv(t)
	m := assocModel(t, dir)

	m.openAssociations()
	if m.dialog.Kind != DialogAssociations {
		t.Fatalf("dialog = %v, want DialogAssociations", m.dialog.Kind)
	}
	sel := m.dialog.AssocShown[m.dialog.ItemIdx]
	if sel.MimeType != "application/pdf" || sel.User || !strings.Contains(m.dialog.Items[m.dialog.ItemIdx], ".pdf") {
		t.Fatalf("cursor on %+v (%q), want the current file's type, the system's choice", sel, m.dialog.Items[m.dialog.ItemIdx])
	}

	// Change it: the chooser lists the apps declaring PDF first, then the
	// others, dimmed.
	press(m, "enter")
	if m.dialog.Kind != DialogChooseApp || !m.dialog.ChooseAppSetOnly {
		t.Fatalf("dialog = %v, want the chooser, setting only", m.dialog.Kind)
	}
	if got := strings.Join(m.dialog.Items, ","); !strings.HasPrefix(got, "Evince") || !strings.Contains(got, "Okular") || !strings.HasSuffix(got, "Editor") {
		t.Errorf("chooser items = %q", got)
	}
	press(m, "edi", "enter")
	if m.dialog.Kind != DialogAssociations {
		t.Fatalf("after choosing, dialog = %v, want back to the list", m.dialog.Kind)
	}
	data, _ := os.ReadFile(mimeapps)
	if !strings.Contains(string(data), "[Default Applications]\napplication/pdf=editor.desktop;") {
		t.Errorf("mimeapps.list:\n%s", data)
	}
	sel = m.dialog.AssocShown[m.dialog.ItemIdx]
	if sel.MimeType != "application/pdf" || !sel.User || sel.App.Name != "Editor" {
		t.Errorf("after choosing, row = %+v", sel)
	}

	// Only the user's choices.
	press(m, "tab")
	if len(m.dialog.Items) != 1 || m.dialog.AssocShown[0].MimeType != "application/pdf" {
		t.Errorf("mine view = %q", m.dialog.Items)
	}
	press(m, "tab")

	// Reset to the system's.
	press(m, "delete")
	sel = m.dialog.AssocShown[m.dialog.ItemIdx]
	if sel.User || sel.App.Name == "Editor" {
		t.Errorf("after reset, row = %+v", sel)
	}
	data, _ = os.ReadFile(mimeapps)
	if strings.Contains(string(data), "application/pdf") {
		t.Errorf("mimeapps.list after reset:\n%s", data)
	}
}

func TestAssociationsDialogFilterByExtension(t *testing.T) {
	_, dir := assocTestEnv(t)
	m := assocModel(t, dir)
	m.openAssociations()

	press(m, ".pn")
	if len(m.dialog.Items) != 1 || m.dialog.AssocShown[0].MimeType != "image/png" {
		t.Errorf("filter .pn = %q", m.dialog.Items)
	}
	// A known extension no application declares gets a row to set one.
	m.dialog.Inputs[0].SetValue("")
	m.filterAssociations("")
	press(m, "md")
	if len(m.dialog.Items) != 1 || m.dialog.AssocShown[0].MimeType != "text/markdown" || m.dialog.AssocShown[0].HasApp {
		t.Fatalf("filter md = %q", m.dialog.Items)
	}
	press(m, "enter")
	if m.dialog.Kind != DialogChooseApp || m.dialog.ChooseAppMime != "text/markdown" {
		t.Errorf("dialog = %v (%s), want the chooser for text/markdown", m.dialog.Kind, m.dialog.ChooseAppMime)
	}
	press(m, "esc")
	if m.dialog.Kind != DialogAssociations || m.dialog.Inputs[0].Value() != "md" {
		t.Errorf("Esc from the chooser: dialog = %v, filter %q", m.dialog.Kind, m.dialog.Inputs[0].Value())
	}
}

func TestAppChooserScrolls(t *testing.T) {
	m := newTestModel()
	m.height = 30
	rows := m.dialogListRows()
	m.dialog = Dialog{Kind: DialogChooseApp, Items: make([]string, rows*3)}
	for i := 0; i < rows+2; i++ {
		m.moveListCursor("down")
	}
	if m.dialog.ItemIdx != rows+2 || m.dialog.ListTop != 3 {
		t.Errorf("ItemIdx %d, ListTop %d; want %d, 3", m.dialog.ItemIdx, m.dialog.ListTop, rows+2)
	}
	m.moveListCursor("end")
	if m.dialog.ListTop != rows*2 {
		t.Errorf("at the end, ListTop %d, want %d", m.dialog.ListTop, rows*2)
	}
	m.moveListCursor("home")
	if m.dialog.ItemIdx != 0 || m.dialog.ListTop != 0 {
		t.Errorf("at home, ItemIdx %d, ListTop %d", m.dialog.ItemIdx, m.dialog.ListTop)
	}
}

func TestNoExtensionRecognizedByContent(t *testing.T) {
	_, dir := assocTestEnv(t)
	m := assocModel(t, dir)
	for i, e := range m.panes[m.active].Entries {
		if e.Name == "scan" {
			m.panes[m.active].Cursor = i
		}
	}

	// Associations: the cursor on the type its content tells.
	m.openAssociations()
	if sel := m.dialog.AssocShown[m.dialog.ItemIdx]; sel.MimeType != "application/pdf" {
		t.Errorf("associations on a PDF with no extension: cursor on %s", sel.MimeType)
	}

	// Opening it: with the application declaring that type.
	m.dialog = Dialog{}
	e, _ := m.panes[m.active].CurrentEntry()
	m.openWithDefaultApp(e)
	if m.dialog.Kind != DialogNone || m.status != "Opened scan with Evince" {
		t.Errorf("dialog = %v, status %q; want scan opened with Evince", m.dialog.Kind, m.status)
	}

	// Text with no application of its own: listed anyway, to choose one.
	m.dialog = Dialog{}
	for i, e := range m.panes[m.active].Entries {
		if e.Name == "notes" {
			m.panes[m.active].Cursor = i
		}
	}
	m.openAssociations()
	if sel := m.dialog.AssocShown[m.dialog.ItemIdx]; sel.MimeType != "text/plain" || sel.HasApp {
		t.Errorf("associations on plain text: cursor on %+v", sel)
	}

	// Opening it: the chooser for that type, none being set.
	m.dialog = Dialog{}
	e, _ = m.panes[m.active].CurrentEntry()
	m.openWithDefaultApp(e)
	if m.dialog.Kind != DialogChooseApp || m.dialog.ChooseAppMime != "text/plain" {
		t.Errorf("dialog = %v for %q, want the chooser for text/plain", m.dialog.Kind, m.dialog.ChooseAppMime)
	}
}

func TestDetectedInBackground(t *testing.T) {
	_, dir := assocTestEnv(t)
	m := assocModel(t, dir)
	remote := &remoteOpenTarget{fs: m.panes[m.active].FS, path: filepath.Join(dir, "notes"), name: "notes"}

	// A remote source is read in the background; the result carries on.
	m.detectInBackground(remote.fs, remote.path, detectedType{name: "notes", remote: remote})
	msg := <-m.openCh
	if msg.detected == nil || msg.detected.mime != "text/plain" {
		t.Fatalf("detected = %+v", msg.detected)
	}
	m.handleOpenResult(msg)
	if m.dialog.Kind != DialogChooseApp || m.dialog.ChooseAppRemote != remote {
		t.Errorf("dialog = %v, want the chooser for the remote file", m.dialog.Kind)
	}

	// A dialog opened meanwhile stays.
	m.dialog = Dialog{Kind: DialogHelp}
	m.handleOpenResult(msg)
	if m.dialog.Kind != DialogHelp || !strings.Contains(m.status, "open it again") {
		t.Errorf("dialog = %v, status %q", m.dialog.Kind, m.status)
	}

	// For the associations dialog: its cursor goes on the type.
	m.dialog = Dialog{}
	m.showAssociations("", false, "")
	m.detectInBackground(remote.fs, remote.path, detectedType{name: "notes", forAssoc: true})
	m.handleOpenResult(<-m.openCh)
	if sel := m.dialog.AssocShown[m.dialog.ItemIdx]; sel.MimeType != "text/plain" {
		t.Errorf("associations: cursor on %s", sel.MimeType)
	}
}
