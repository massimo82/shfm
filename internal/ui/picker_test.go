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
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/config"
	"shfm/internal/pick"
)

// pickerTree makes a synthetic tree, with HOME and the config inside it:
//
//	root/a.pdf, root/b.txt, root/.hidden, root/sub/c.pdf
func pickerTree(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", filepath.Join(tmp, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	root := filepath.Join(tmp, "root")
	for _, f := range []string{"a.pdf", "b.txt", ".hidden", "sub/c.pdf"} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func newPickerModel(t *testing.T, req pick.Request) *Model {
	t.Helper()
	m := New(config.Default(), config.DefaultKeyMap(), Start{Pick: &req})
	m.width, m.height = 120, 40
	return m
}

func names(p *Pane) []string {
	var out []string
	for _, e := range p.Entries {
		out = append(out, e.Name)
	}
	return out
}

func cursorOn(t *testing.T, p *Pane, name string) {
	t.Helper()
	for i, e := range p.Entries {
		if e.Name == name {
			p.Cursor = i
			return
		}
	}
	t.Fatalf("%q not listed: %q", name, names(p))
}

func wantReply(t *testing.T, m *Model, paths ...string) {
	t.Helper()
	r, ok := m.PickReply()
	if !ok {
		t.Fatalf("no choice made; status %q", m.status)
	}
	if !reflect.DeepEqual(r.Paths, paths) {
		t.Fatalf("chosen %q, want %q", r.Paths, paths)
	}
	if !m.quitting {
		t.Fatal("shfm should quit once the choice is made")
	}
}

func TestResolveStart(t *testing.T) {
	root := pickerTree(t)
	home := os.Getenv("HOME")
	for _, tc := range []struct {
		name      string
		start     Start
		wantDir   string
		wantNames []string
	}{
		{"folder", Start{Paths: []string{root}}, root, nil},
		{"file", Start{Paths: []string{filepath.Join(root, "a.pdf")}}, root, []string{"a.pdf"}},
		{"file URI", Start{Paths: []string{"file://" + root + "/sub/c.pdf"}}, filepath.Join(root, "sub"), []string{"c.pdf"}},
		{"folder selected", Start{Paths: []string{filepath.Join(root, "sub")}, Select: true}, root, []string{"sub"}},
		{"folder properties", Start{Paths: []string{filepath.Join(root, "sub")}, Properties: true}, root, []string{"sub"}},
		{"several, one elsewhere", Start{Paths: []string{
			filepath.Join(root, "a.pdf"), filepath.Join(root, "sub/c.pdf"), filepath.Join(root, "b.txt"),
		}}, root, []string{"a.pdf", "b.txt"}},
		{"deleted file", Start{Paths: []string{filepath.Join(root, "gone.zip")}}, root, nil},
		{"nothing valid", Start{Paths: []string{"/nonexistent/x/y"}}, home, nil},
		{"not a file URI", Start{Paths: []string{"smb://nas/share"}}, home, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, got := resolveStart(tc.start)
			if dir != tc.wantDir || !reflect.DeepEqual(got, tc.wantNames) {
				t.Fatalf("resolveStart = %q, %q; want %q, %q", dir, got, tc.wantDir, tc.wantNames)
			}
		})
	}
}

func TestStartRevealsItems(t *testing.T) {
	root := pickerTree(t)

	m := New(config.Default(), config.DefaultKeyMap(), Start{Paths: []string{filepath.Join(root, "b.txt")}})
	p := m.activePane()
	if e, _ := p.CurrentEntry(); e.Name != "b.txt" || len(p.Selected) != 0 {
		t.Fatalf("cursor on %q, selected %v; want the cursor on b.txt, nothing selected", e.Name, p.Selected)
	}

	m = New(config.Default(), config.DefaultKeyMap(), Start{Paths: []string{filepath.Join(root, "b.txt"), filepath.Join(root, "a.pdf")}})
	p = m.activePane()
	if e, _ := p.CurrentEntry(); e.Name != "a.pdf" || !p.Selected["a.pdf"] || !p.Selected["b.txt"] {
		t.Fatalf("cursor on %q, selected %v; want both selected, cursor on the first listed", e.Name, p.Selected)
	}

	m = New(config.Default(), config.DefaultKeyMap(), Start{Paths: []string{filepath.Join(root, ".hidden")}})
	p = m.activePane()
	if e, _ := p.CurrentEntry(); e.Name != ".hidden" {
		t.Fatalf("a hidden file asked for should be shown, cursor on %q", e.Name)
	}
	if m.cfg.ShowHidden {
		t.Fatal("showing it must not change the saved preference")
	}

	m = New(config.Default(), config.DefaultKeyMap(), Start{Paths: []string{filepath.Join(root, "sub")}, Properties: true})
	if m.dialog.Kind != DialogProperties || filepath.Base(m.dialog.PropsPath) != "sub" {
		t.Fatalf("dialog %v on %q, want the properties of sub", m.dialog.Kind, m.dialog.PropsPath)
	}
}

func TestPickOpenFile(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeOpen, CurrentFolder: root, CurrentFilter: -1})
	p := m.activePane()

	// Enter on a folder still opens it.
	cursorOn(t, p, "sub")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if p.Path != filepath.Join(root, "sub") {
		t.Fatalf("Enter on a folder should open it, now in %s", p.Path)
	}
	cursorOn(t, p, "c.pdf")
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	wantReply(t, m, filepath.Join(root, "sub", "c.pdf"))
	if cmd == nil {
		t.Fatal("choosing should quit the program")
	}
}

func TestPickOpenMultiple(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeOpen, Multiple: true, CurrentFolder: root, CurrentFilter: -1})
	p := m.activePane()
	p.Selected["a.pdf"], p.Selected["b.txt"], p.Selected["sub"] = true, true, true
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	wantReply(t, m, filepath.Join(root, "a.pdf"), filepath.Join(root, "b.txt"))
}

func TestPickOpenSingleRejectsSelection(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeOpen, CurrentFolder: root, CurrentFilter: -1})
	p := m.activePane()
	p.Selected["a.pdf"], p.Selected["b.txt"] = true, true
	m.pickAccept()
	if _, ok := m.PickReply(); ok || !m.statusErr {
		t.Fatal("two files can't be chosen when the application wants one")
	}
}

func TestPickFilters(t *testing.T) {
	root := pickerTree(t)
	req := pick.Request{
		Mode: pick.ModeOpen, CurrentFolder: root, CurrentFilter: 0,
		Filters: []pick.Filter{
			{Name: "PDF", Patterns: []pick.Pattern{{Kind: pick.PatternGlob, Pattern: "*.pdf"}}},
			{Name: "Text", Patterns: []pick.Pattern{{Kind: pick.PatternMIME, Pattern: "text/plain"}}},
		},
	}
	m := newPickerModel(t, req)
	p := m.activePane()
	if got := names(p); !reflect.DeepEqual(got, []string{"..", "sub", "a.pdf"}) {
		t.Fatalf("listed %q with the PDF filter", got)
	}
	m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if m.dialog.Kind != DialogPickFilter {
		t.Fatalf("dialog %v, want the file type list", m.dialog.Kind)
	}
	m.dialog.ItemIdx = 1
	m.confirmDialog()
	if got := names(p); !reflect.DeepEqual(got, []string{"..", "sub", "b.txt"}) {
		t.Fatalf("listed %q with the Text filter", got)
	}
	cursorOn(t, p, "b.txt")
	m.enterOrOpen()
	wantReply(t, m, filepath.Join(root, "b.txt"))
	if r, _ := m.PickReply(); r.Filter != 1 {
		t.Fatalf("filter in use %d, want 1", r.Filter)
	}
}

func TestPickFolder(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeOpen, Directory: true, CurrentFolder: root, CurrentFilter: -1})
	p := m.activePane()
	if got := names(p); !reflect.DeepEqual(got, []string{"..", "sub"}) {
		t.Fatalf("listed %q, want folders only", got)
	}
	cursorOn(t, p, "sub")
	m.enterOrOpen()
	if _, ok := m.PickReply(); ok {
		t.Fatal("Enter on a folder should open it, not choose it")
	}
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	wantReply(t, m, filepath.Join(root, "sub"))
}

func TestPickSave(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeSave, CurrentFolder: root, CurrentName: "new.pdf", CurrentFilter: -1})
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if m.dialog.Kind != DialogPickSaveName || m.dialog.Inputs[0].Value() != "new.pdf" {
		t.Fatalf("dialog %v with %q, want the name dialog with the suggested name", m.dialog.Kind, m.dialog.Inputs[0].Value())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	wantReply(t, m, filepath.Join(root, "new.pdf"))
}

func TestPickSaveReplace(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeSave, CurrentFolder: root, CurrentName: "new.pdf", CurrentFilter: -1})
	p := m.activePane()
	cursorOn(t, p, "a.pdf")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.dialog.Kind != DialogPickSaveName || m.dialog.Inputs[0].Value() != "a.pdf" {
		t.Fatalf("Enter on a file: dialog %v, want the name dialog with its name", m.dialog.Kind)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.dialog.Kind != DialogPickOverwrite {
		t.Fatalf("dialog %v, want the replace confirmation", m.dialog.Kind)
	}
	m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	wantReply(t, m, filepath.Join(root, "a.pdf"))
}

func TestPickSaveFolderName(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeSave, CurrentFolder: root, CurrentFilter: -1})
	m.askPickSaveName("sub")
	m.confirmDialog()
	if p := m.activePane(); p.Path != filepath.Join(root, "sub") || m.dialog.Kind != DialogNone {
		t.Fatalf("a folder's name should open it: in %s, dialog %v", p.Path, m.dialog.Kind)
	}
	if _, ok := m.PickReply(); ok {
		t.Fatal("nothing should be chosen yet")
	}
}

func TestPickSaveFiles(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeSaveFiles, CurrentFolder: root, Files: []string{"x.txt", "a.pdf"}, CurrentFilter: -1})
	m.pickAccept()
	if m.dialog.Kind != DialogPickOverwrite {
		t.Fatalf("dialog %v, want the replace confirmation for a.pdf", m.dialog.Kind)
	}
	m.confirmDialog()
	wantReply(t, m, filepath.Join(root, "x.txt"), filepath.Join(root, "a.pdf"))
}

func TestPickOptions(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeOpen, CurrentFolder: root, CurrentFilter: -1, Choices: []pick.Choice{
		{ID: "ro", Label: "Read only"},
		{ID: "enc", Label: "Encoding", Options: []pick.ChoiceOption{{ID: "utf8", Label: "UTF-8"}, {ID: "latin1", Label: "Latin-1"}}},
	}})
	m.Update(tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if m.dialog.Kind != DialogPickOptions {
		t.Fatalf("dialog %v, want the options", m.dialog.Kind)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if want := []string{"[x] Read only", "Encoding: Latin-1"}; !reflect.DeepEqual(m.dialog.Items, want) {
		t.Fatalf("options shown %q, want %q", m.dialog.Items, want)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	cursorOn(t, m.activePane(), "a.pdf")
	m.enterOrOpen()
	r, _ := m.PickReply()
	if want := map[string]string{"ro": "true", "enc": "latin1"}; !reflect.DeepEqual(r.Choices, want) {
		t.Fatalf("choices %v, want %v", r.Choices, want)
	}
}

func TestPickCancel(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeOpen, CurrentFolder: root, CurrentFilter: -1})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, ok := m.PickReply(); ok || !m.quitting || cmd == nil {
		t.Fatal("Esc should cancel the file chooser")
	}
}

func TestPickTrashRefused(t *testing.T) {
	root := pickerTree(t)
	m := newPickerModel(t, pick.Request{Mode: pick.ModeOpen, Directory: true, CurrentFolder: root, CurrentFilter: -1})
	m.activePane().Mode = PaneTrash
	m.pickAccept()
	if _, ok := m.PickReply(); ok || !m.statusErr {
		t.Fatal("nothing can be chosen in the trash")
	}
}
