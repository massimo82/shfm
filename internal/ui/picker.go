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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"shfm/internal/config"
	"shfm/internal/pick"
	"shfm/internal/vfs"
)

// File chooser mode (`shfm --pick`, run by the file chooser portal backend,
// see internal/portal): shfm works as usual, but choosing — Enter or a
// double click on a file, or the pick-accept key (Ctrl+O) — ends it,
// handing the choice back to the application; Esc (with no search or
// filter to cancel) or quitting cancels.
//
//   - Opening files: the file under the cursor, or the selected ones when
//     several may be chosen. Files not matching the current file type
//     (pick-filter, Ctrl+T) aren't listed.
//   - Opening folders: only folders are listed; the selected ones, or else
//     the current folder.
//   - Saving a file: a name in the current folder, asked in a dialog
//     (pre-filled with the application's suggestion, or the name of the
//     file Enter was pressed on), with a confirmation to replace an
//     existing file.
//   - Saving several files: the current folder, with a confirmation if
//     some of them would replace existing files.
//
// Only local folders can be chosen: the application needs a path it can
// still open once shfm, and with it any FUSE mount of a network source,
// is gone.

// pickerState is the file chooser request being answered.
type pickerState struct {
	req     pick.Request
	filter  int               // index in req.Filters, -1 for none
	choices map[string]string // current value of each of req.Choices, by ID
	reply   *pick.Reply       // set once the user has chosen
}

// PickReply returns the user's choice, once shfm run as a file chooser has
// ended; ok is false if the user cancelled.
func (m *Model) PickReply() (pick.Reply, bool) {
	if m.picker == nil || m.picker.reply == nil {
		return pick.Reply{}, false
	}
	return *m.picker.reply, true
}

// pickStartPaths returns where the file chooser opens: on the file being
// saved again, selected, or in the folder the application suggests.
func pickStartPaths(req pick.Request) (paths []string, selectItems bool) {
	switch {
	case req.CurrentFile != "":
		return []string{req.CurrentFile}, true
	case req.CurrentFolder != "":
		return []string{req.CurrentFolder}, false
	}
	return nil, false
}

func (m *Model) startPicker(req pick.Request) {
	m.picker = &pickerState{req: req, filter: req.CurrentFilter, choices: pick.DefaultChoices(req.Choices)}
	// Without one chosen, the first file type applies, as in GTK's
	// dialog (applications usually add an "All files" one of their own).
	if m.picker.filter < 0 || m.picker.filter >= len(req.Filters) {
		m.picker.filter = -1
		if len(req.Filters) > 0 {
			m.picker.filter = 0
		}
	}
	m.applyPickFilter()
}

func (ps *pickerState) foldersOnly() bool {
	return (ps.req.Mode == pick.ModeOpen && ps.req.Directory) || ps.req.Mode == pick.ModeSaveFiles
}

// applyPickFilter makes both panes list only what can be chosen.
func (m *Model) applyPickFilter() {
	var f func(vfs.Entry) bool
	switch ps := m.picker; {
	case ps.foldersOnly():
		f = func(e vfs.Entry) bool { return e.IsDir }
	case ps.filter >= 0:
		filter := ps.req.Filters[ps.filter]
		f = func(e vfs.Entry) bool { return e.IsDir || filter.Match(e.Name) }
	}
	for _, p := range m.panes {
		p.EntryFilter = f
		if p.Mode == PaneNormal && !p.ShowingSearchResults {
			cur, hadCur := p.CurrentEntry()
			p.Load()
			if hadCur {
				for i, e := range p.Entries {
					if e.Name == cur.Name {
						p.Cursor = i
						break
					}
				}
			}
		}
	}
}

// pickTitle is the title bar's text in file chooser mode.
func (m *Model) pickTitle() string {
	req := m.picker.req
	title := req.Title
	if title == "" {
		switch {
		case req.Mode == pick.ModeSave:
			title = "Save File"
		case req.Mode == pick.ModeSaveFiles:
			title = "Save Files"
		case req.Directory && req.Multiple:
			title = "Choose Folders"
		case req.Directory:
			title = "Choose a Folder"
		case req.Multiple:
			title = "Open Files"
		default:
			title = "Open a File"
		}
	}
	s := "  shfm — " + title
	if req.Mode == pick.ModeSaveFiles {
		s += fmt.Sprintf("  ·  %d file(s) to save", len(req.Files))
	}
	if ps := m.picker; ps.filter >= 0 {
		s += "  ·  Type: " + ps.req.Filters[ps.filter].Name
	}
	return s
}

// pickHints is the help line's text in file chooser mode.
func (m *Model) pickHints() string {
	req := m.picker.req
	accept := m.firstKey(config.ActionPickAccept)
	label := req.AcceptLabel
	var parts []string
	switch {
	case req.Mode == pick.ModeSave:
		if label == "" {
			label = "save"
		}
		parts = append(parts, accept+" "+strings.ToLower(label)+" here", "Enter on a file: replace it")
	case m.picker.foldersOnly():
		if label == "" {
			label = "choose"
		}
		what := "this folder"
		if req.Mode == pick.ModeOpen && req.Multiple {
			what = "the selected folders, or this one"
		}
		parts = append(parts, accept+" "+strings.ToLower(label)+" "+what, "Enter open folder")
	default:
		if label == "" {
			label = "choose"
		}
		parts = append(parts, "Enter/double click "+strings.ToLower(label)+" file")
		if req.Multiple {
			parts = append(parts, "Space select · "+accept+" "+strings.ToLower(label)+" selected")
		}
	}
	if len(req.Filters) > 1 {
		parts = append(parts, m.firstKey(config.ActionPickFilter)+" file type")
	}
	if len(req.Choices) > 0 {
		parts = append(parts, m.firstKey(config.ActionPickOptions)+" options")
	}
	parts = append(parts, m.firstKey(config.ActionCancel)+" cancel")
	return strings.Join(parts, " · ")
}

func (m *Model) firstKey(a config.Action) string {
	if keys := m.keymap.KeysFor(a); len(keys) > 0 {
		label := prettyKey(keys[0])
		// "Ctrl+O", as the help line spells shortcuts: case only
		// matters for a letter without modifiers.
		if i := strings.LastIndex(label, "+"); i >= 0 && len(label) == i+2 {
			label = label[:i+1] + strings.ToUpper(label[i+1:])
		}
		return label
	}
	return "(" + string(a) + ")"
}

// pickDir returns the local path of p's current folder, or reports why
// nothing can be chosen there.
func (m *Model) pickDir(p *Pane) (string, bool) {
	if p.Mode != PaneNormal {
		m.setError("Nothing can be chosen in the trash")
		return "", false
	}
	if lp, ok := p.FS.(vfs.LocalPath); ok {
		if dir, ok := lp.LocalPath(p.Path); ok {
			return dir, true
		}
	}
	m.setError("Only local folders can be chosen: the application couldn't open %s once shfm is closed", p.SourceLabel)
	return "", false
}

// entryByName finds a listed entry of p.
func entryByName(p *Pane, name string) (vfs.Entry, bool) {
	for _, e := range p.Entries {
		if e.Name == name {
			return e, true
		}
	}
	return vfs.Entry{}, false
}

// pickAccept is the pick-accept key.
func (m *Model) pickAccept() {
	p := m.activePane()
	dir, ok := m.pickDir(p)
	if !ok {
		return
	}
	req := m.picker.req
	switch {
	case req.Mode == pick.ModeSave:
		name := req.CurrentName
		if name == "" && req.CurrentFile != "" {
			name = filepath.Base(req.CurrentFile)
		}
		m.askPickSaveName(name)

	case req.Mode == pick.ModeSaveFiles:
		var paths, existing []string
		for _, f := range req.Files {
			target := filepath.Join(dir, filepath.Base(f))
			paths = append(paths, target)
			if _, err := os.Lstat(target); err == nil {
				existing = append(existing, filepath.Base(f))
			}
		}
		if len(existing) > 0 {
			m.askPickOverwrite(paths, fmt.Sprintf("%d of the files already exist in this folder:\n%s\n\nReplace them?",
				len(existing), strings.Join(limitLines(existing, 8), "\n")))
			return
		}
		m.pickFinish(paths)

	case req.Directory:
		var paths []string
		for _, name := range sortedSelection(p) {
			if e, ok := entryByName(p, name); ok && e.IsDir {
				paths = append(paths, filepath.Join(dir, name))
			}
		}
		if len(paths) == 0 {
			paths = []string{dir}
		}
		if len(paths) > 1 && !req.Multiple {
			m.setError("Only one folder can be chosen")
			return
		}
		m.pickFinish(paths)

	default:
		var paths []string
		for _, name := range p.SelectedNames() {
			if e, ok := entryByName(p, name); ok && !e.IsDir {
				paths = append(paths, filepath.Join(dir, name))
			}
		}
		switch {
		case len(paths) == 0:
			m.setError("Choose a file: Enter or double click on it")
		case len(paths) > 1 && !req.Multiple:
			m.setError("Only one file can be chosen")
		default:
			m.pickFinish(paths)
		}
	}
}

// pickActivateFile is Enter or a double click on a file.
func (m *Model) pickActivateFile(e vfs.Entry) {
	p := m.activePane()
	req := m.picker.req
	switch {
	case req.Mode == pick.ModeSave:
		if _, ok := m.pickDir(p); ok {
			m.askPickSaveName(e.Name)
		}
	case req.Mode == pick.ModeOpen && !req.Directory:
		if req.Multiple && p.Selected[e.Name] {
			m.pickAccept() // the file is part of the selection: take it all
			return
		}
		if dir, ok := m.pickDir(p); ok {
			m.pickFinish([]string{filepath.Join(dir, e.Name)})
		}
	}
}

// pickCancel ends shfm without a choice.
func (m *Model) pickCancel() {
	m.quitting = true
}

// pickFinish ends shfm with paths as the choice.
func (m *Model) pickFinish(paths []string) {
	m.picker.reply = &pick.Reply{Paths: paths, Filter: m.picker.filter, Choices: m.picker.choices}
	m.dialog = Dialog{}
	m.quitting = true
}

func (m *Model) askPickSaveName(name string) {
	title := "Save as"
	if l := m.picker.req.AcceptLabel; l != "" {
		title = strings.TrimSuffix(strings.ReplaceAll(l, "_", ""), "…")
	}
	d := newSingleInputDialog(DialogPickSaveName, title, "file name", name)
	// Put the cursor before the extension, so typing renames the file
	// while keeping its type.
	if ext := filepath.Ext(name); ext != "" && ext != name {
		d.Inputs[0].SetCursor(len([]rune(name)) - len([]rune(ext)))
	}
	m.dialog = d
}

func (m *Model) askPickOverwrite(paths []string, message string) {
	m.dialog = Dialog{Kind: DialogPickOverwrite, Title: "Replace?", Message: message, PickPaths: paths}
}

// confirmPickSaveName handles the name typed in the save dialog.
func (m *Model) confirmPickSaveName() {
	name := strings.TrimSpace(m.dialog.Inputs[0].Value())
	if name == "" {
		return
	}
	p := m.activePane()
	dir, ok := m.pickDir(p)
	if !ok {
		m.dialog = Dialog{}
		return
	}
	target := filepath.Clean(filepath.Join(dir, name))
	if filepath.IsAbs(name) {
		target = filepath.Clean(name)
	}
	info, err := os.Stat(target)
	switch {
	case err == nil && info.IsDir():
		// A folder's name: go there, as GTK's dialog does.
		m.dialog = Dialog{}
		m.pickNavigate(p, target)
		m.setStatus("Choose the file name in %s", target)
	case err == nil:
		m.askPickOverwrite([]string{target}, fmt.Sprintf("“%s” already exists.\n\nReplace it?", filepath.Base(target)))
	default:
		if info, err := os.Stat(filepath.Dir(target)); err != nil || !info.IsDir() {
			m.dialog.Message = "The folder " + filepath.Dir(target) + " doesn't exist"
			m.dialog.IsError = true
			return
		}
		m.pickFinish([]string{target})
	}
}

// pickNavigate opens local folder dir in p.
func (m *Model) pickNavigate(p *Pane, dir string) {
	p.Path = dir
	p.Cursor, p.Offset = 0, 0
	p.DeselectAll()
	p.FilterQuery, p.FilterActive = "", false
	p.Load()
}

// openPickFilter opens the file type list.
func (m *Model) openPickFilter() {
	ps := m.picker
	if ps.foldersOnly() || len(ps.req.Filters) == 0 {
		m.setStatus("There are no file types to choose from")
		return
	}
	items := make([]string, len(ps.req.Filters))
	for i, f := range ps.req.Filters {
		items[i] = f.Name
	}
	m.dialog = Dialog{Kind: DialogPickFilter, Title: "File type", Items: items, ItemIdx: maxInt(ps.filter, 0)}
}

func (m *Model) confirmPickFilter() {
	m.picker.filter = m.dialog.ItemIdx
	m.dialog = Dialog{}
	m.applyPickFilter()
}

// openPickOptions opens the application's extra options.
func (m *Model) openPickOptions() {
	if len(m.picker.req.Choices) == 0 {
		m.setStatus("The application offers no options")
		return
	}
	m.dialog = Dialog{Kind: DialogPickOptions, Title: "Options", Items: m.pickOptionItems()}
}

func (m *Model) pickOptionItems() []string {
	ps := m.picker
	items := make([]string, len(ps.req.Choices))
	for i, c := range ps.req.Choices {
		v := ps.choices[c.ID]
		if len(c.Options) == 0 {
			box := "[ ]"
			if v == "true" {
				box = "[x]"
			}
			items[i] = box + " " + c.Label
			continue
		}
		label := v
		for _, o := range c.Options {
			if o.ID == v {
				label = o.Label
			}
		}
		items[i] = c.Label + ": " + label
	}
	return items
}

// cyclePickOption toggles the highlighted checkbox, or moves the
// highlighted choice to its next value; the dialog stays open.
func (m *Model) cyclePickOption() {
	ps := m.picker
	idx := m.dialog.ItemIdx
	if idx < 0 || idx >= len(ps.req.Choices) {
		return
	}
	c := ps.req.Choices[idx]
	v := ps.choices[c.ID]
	if len(c.Options) == 0 {
		if v == "true" {
			ps.choices[c.ID] = "false"
		} else {
			ps.choices[c.ID] = "true"
		}
	} else {
		next := 0
		for i, o := range c.Options {
			if o.ID == v {
				next = (i + 1) % len(c.Options)
			}
		}
		ps.choices[c.ID] = c.Options[next].ID
	}
	m.dialog.Items = m.pickOptionItems()
}

// sortedSelection returns p's selected names in listing order.
func sortedSelection(p *Pane) []string {
	var out []string
	for _, e := range p.Entries {
		if p.Selected[e.Name] {
			out = append(out, e.Name)
		}
	}
	return out
}

func limitLines(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return append(append([]string{}, lines[:n]...), fmt.Sprintf("… and %d more", len(lines)-n))
}

// pickKey handles the file chooser's own actions; handled is false for
// every other action, and outside file chooser mode.
func (m *Model) pickKey(action config.Action) (handled bool) {
	if m.picker == nil {
		return false
	}
	switch action {
	case config.ActionPickAccept:
		m.pickAccept()
	case config.ActionPickFilter:
		m.openPickFilter()
	case config.ActionPickOptions:
		m.openPickOptions()
	default:
		return false
	}
	return true
}
