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

	"shfm/internal/cloud"
	"shfm/internal/config"
)

// TestPasteIntoDialogFields: text pasted in the terminal lands in the
// focused field of a dialog, without the line break copying took along.
func TestPasteIntoDialogFields(t *testing.T) {
	isolateCloudTest(t)
	m := newTestModel()

	p, _ := cloud.Provider(cloud.GoogleDrive)
	m.dialog = newCloudAccountDialog(p, config.CloudSource{}, "", "", "")
	if !cloud.Available {
		m.dialog = newConnectDialog(DialogConnectSMB)
	}
	m.Update(tea.PasteMsg{Content: "1234-abc.apps.googleusercontent.com\n"})
	if got := m.dialog.Inputs[0].Value(); got != "1234-abc.apps.googleusercontent.com" {
		t.Errorf("first field = %q", got)
	}
	m.updateDialogKey(tea.KeyPressMsg{Code: tea.KeyTab})
	m.Update(tea.PasteMsg{Content: "GOCSPX-s3cret\r\n"})
	if got := m.dialog.Inputs[1].Value(); got != "GOCSPX-s3cret" {
		t.Errorf("second field = %q", got)
	}

	// A password field of a connection form.
	m.dialog = newConnectDialog(DialogConnectSMB)
	focusConnectField(&m.dialog, 4)
	m.Update(tea.PasteMsg{Content: "pa ss"})
	if got := m.dialog.Inputs[4].Value(); got != "pa ss" {
		t.Errorf("password = %q", got)
	}
}

// TestPasteUpdatesLiveFilter: pasting a search query filters the folder
// as typing it does.
func TestPasteUpdatesLiveFilter(t *testing.T) {
	isolateCloudTest(t)
	dir := t.TempDir()
	for _, n := range []string{"report.txt", "photo.jpg"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	m := newTestModel()
	p := m.activePane()
	p.Path = dir
	p.Load()
	m.openSearch()
	m.Update(tea.PasteMsg{Content: "report"})
	var shown []string
	for _, e := range p.Entries {
		if !IsParentEntry(e) {
			shown = append(shown, e.Name)
		}
	}
	if len(shown) != 1 || shown[0] != "report.txt" {
		t.Errorf("after pasting the query: %v", shown)
	}
}

// TestPasteIntoPathField: a path pasted while editing PATH.
func TestPasteIntoPathField(t *testing.T) {
	isolateCloudTest(t)
	m := newTestModel()
	p := m.activePane()
	p.BeginPathEdit()
	p.PathInput.SetValue("")
	m.Update(tea.PasteMsg{Content: "/tmp\n"})
	if got := p.PathInput.Value(); got != "/tmp" {
		t.Errorf("PATH = %q", got)
	}
}
