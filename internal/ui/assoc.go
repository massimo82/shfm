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
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"shfm/internal/opener"
)

// File associations: which application opens each file type, listed by
// extension. The associations dialog shows every type the system knows an
// application for (or only the user's own choices), filtered by typing;
// from it one can change a type's application — through the same chooser
// opening a file with no default uses — or reset it to the system's.

// Rows above the first list item in each dialog's body, as rendered by
// renderAssociations/renderChooseApp: handleDialogMouse maps clicks with
// these. Both reserve a row for the "↑ more" marker of a scrolled list.
const (
	assocListTop     = 5 // intro, blank, filter, blank, "↑ more"
	chooseAppListTop = 5 // message, blank, filter, blank, "↑ more"
)

// dialogListRows is how many items a scrolling dialog list shows at once,
// sized to the terminal.
func (m *Model) dialogListRows() int {
	return max(4, min(18, m.height-18))
}

// scrollListTop returns the first visible item of a list of n items
// showing rows at a time, moved from top as little as needed to keep idx
// visible.
func scrollListTop(top, idx, rows, n int) int {
	if idx < top {
		top = idx
	}
	if idx >= top+rows {
		top = idx - rows + 1
	}
	return max(0, min(top, n-rows))
}

// moveListCursor applies a navigation key to the dialog's list cursor,
// scrolling the list with it; false when key isn't one.
func (m *Model) moveListCursor(key string) bool {
	d := &m.dialog
	n := len(d.Items)
	rows := m.dialogListRows()
	switch key {
	case "up", "ctrl+k":
		d.ItemIdx--
	case "down", "ctrl+j":
		d.ItemIdx++
	case "pgup":
		d.ItemIdx -= rows
	case "pgdown":
		d.ItemIdx += rows
	case "home":
		d.ItemIdx = 0
	case "end":
		d.ItemIdx = n - 1
	default:
		return false
	}
	d.ItemIdx = max(0, min(d.ItemIdx, n-1))
	d.ListTop = scrollListTop(d.ListTop, d.ItemIdx, rows, n)
	return true
}

// renderScrollList renders items[top:top+rows], the one at idx
// highlighted, framed by "↑/↓ N more" rows (blank when there's nothing
// more that way) so the list's rows never shift. dim marks items to show
// dimmed, when not highlighted (nil: none).
func renderScrollList(b *strings.Builder, items []string, dim []bool, idx, top, rows, width int) {
	end := min(len(items), top+rows)
	if top > 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  \u2191 %d more", top)))
	}
	b.WriteString("\n")
	for i := top; i < end; i++ {
		prefix, s := "  ", styleFile
		if i == idx {
			prefix, s = "\u25b8 ", styleAccent
		} else if dim != nil && dim[i] {
			s = styleDim
		}
		b.WriteString(prefix + s.Render(truncate(items[i], width-2)) + "\n")
	}
	if end < len(items) {
		b.WriteString(styleDim.Render(fmt.Sprintf("  \u2193 %d more", len(items)-end)) + "\n")
	}
}

// --- the associations list ----------------------------------------------------------

// openAssociations opens the file associations dialog, on the type of the
// file under the cursor when there's one.
func (m *Model) openAssociations() {
	current := ""
	p := m.activePane()
	e, ok := p.CurrentEntry()
	if p.Mode != PaneNormal || !ok || e.IsDir || IsParentEntry(e) {
		m.showAssociations("", false, "")
		return
	}
	// A name that doesn't tell the type: its content does (see detect.go).
	path := p.FS.Join(p.Path, e.Name)
	current, ok = opener.MimeTypeByName(e.Name)
	switch {
	case ok:
	case isLocalFS(p.FS):
		current = opener.DetectMimeType(e.Name, readHead(p.FS, path))
	default:
		m.detectInBackground(p.FS, path, detectedType{name: e.Name, forAssoc: true})
	}
	m.showAssociations("", false, current)
}

// showAssociations (re)builds the associations dialog, with query in its
// filter, showing only the user's choices when mine, the cursor on
// selectMime.
func (m *Model) showAssociations(query string, mine bool, selectMime string) {
	ti := textinput.New()
	ti.Placeholder = "extension, type or application"
	ti.CharLimit = 100
	ti.SetWidth(40)
	ti.SetValue(query)
	ti.CursorEnd()
	ti.Focus()
	all := opener.Associations()
	// The type to select is listed even with no application for it (the
	// current file's, say), to choose one.
	listed := selectMime == ""
	for _, a := range all {
		listed = listed || a.MimeType == selectMime
	}
	if !listed {
		all = append([]opener.Association{{MimeType: selectMime, Extensions: opener.Extensions(selectMime)}}, all...)
	}
	m.dialog = Dialog{
		Kind: DialogAssociations, Title: "File associations", Inputs: []textinput.Model{ti},
		AssocAll: all, AssocMine: mine,
	}
	m.filterAssociations(selectMime)
}

// filterAssociations rebuilds the visible rows from the filter, the cursor
// on selectMime when shown. A filter naming a type that isn't listed (an
// extension, or a MIME type) adds a row to set its application.
func (m *Model) filterAssociations(selectMime string) {
	d := &m.dialog
	q := strings.ToLower(strings.TrimSpace(d.Inputs[0].Value()))
	d.AssocShown, d.Items, d.AssocSystem = nil, nil, nil
	d.ItemIdx = 0
	add := func(a opener.Association) {
		if a.MimeType == selectMime {
			d.ItemIdx = len(d.Items)
		}
		d.AssocShown = append(d.AssocShown, a)
		d.Items = append(d.Items, associationLabel(a))
		d.AssocSystem = append(d.AssocSystem, !a.User)
	}
	listed := false
	typed, typedErr := opener.ParseMimeInput(q)
	for _, a := range d.AssocAll {
		if a.MimeType == typed {
			listed = true
		}
		if (d.AssocMine && !a.User) || (q != "" && !associationMatches(a, q)) {
			continue
		}
		add(a)
	}
	if !listed && typedErr == nil {
		add(opener.Association{MimeType: typed, Extensions: opener.Extensions(typed), User: true})
	}
	d.ListTop = scrollListTop(0, d.ItemIdx, m.dialogListRows(), len(d.Items))
}

// associationMatches reports whether a matches the lowercase filter q: in
// one of its extensions (with or without the dot), its MIME type or its
// application's name.
func associationMatches(a opener.Association, q string) bool {
	ext := strings.TrimPrefix(q, ".")
	for _, e := range a.Extensions {
		if strings.HasPrefix(strings.ToLower(strings.TrimPrefix(e, ".")), ext) {
			return true
		}
	}
	return strings.Contains(a.MimeType, q) ||
		(a.HasApp && strings.Contains(strings.ToLower(a.App.Name), q))
}

// associationLabel is a's row in the associations list: its extensions,
// type and application — marked when that's the system's choice rather
// than the user's.
func associationLabel(a opener.Association) string {
	exts := strings.Join(a.Extensions, " ")
	if exts == "" {
		exts = "—"
	}
	app := "(none)"
	if a.HasApp {
		app = a.App.Name
	}
	if !a.User && a.HasApp {
		app += "  · system"
	}
	return fmt.Sprintf("%-18s %-30s %s", truncate(exts, 18), truncate(a.MimeType, 30), app)
}

// updateAssociationsKey handles the associations dialog's keys: Enter
// changes the selected type's application, Delete resets it to the
// system's, Tab switches between all types and the user's choices; any
// other key edits the filter.
func (m *Model) updateAssociationsKey(msg tea.KeyMsg) tea.Cmd {
	d := &m.dialog
	selected := ""
	if d.ItemIdx >= 0 && d.ItemIdx < len(d.AssocShown) {
		selected = d.AssocShown[d.ItemIdx].MimeType
	}
	switch msg.String() {
	case "esc":
		m.dialog = Dialog{}
		return nil
	case "tab":
		d.AssocMine = !d.AssocMine
		m.filterAssociations(selected)
		return nil
	case "enter":
		if selected != "" {
			query, mine := d.Inputs[0].Value(), d.AssocMine
			m.openAppChooser(selected, "", nil)
			m.dialog.AssocQuery, m.dialog.AssocMine = query, mine
		}
		return nil
	case "delete", "ctrl+r":
		m.resetAssociation(selected)
		return nil
	}
	if m.moveListCursor(msg.String()) {
		return nil
	}
	before := d.Inputs[0].Value()
	var cmd tea.Cmd
	d.Inputs[0], cmd = d.Inputs[0].Update(msg)
	if d.Inputs[0].Value() != before {
		m.filterAssociations(selected)
	}
	return cmd
}

// resetAssociation drops the user's choice for mimeType, back to the
// system's.
func (m *Model) resetAssociation(mimeType string) {
	d := &m.dialog
	if mimeType == "" {
		return
	}
	user := false
	for _, a := range d.AssocAll {
		user = user || (a.MimeType == mimeType && a.User)
	}
	if !user {
		m.setStatus("%s already uses the system's default", mimeType)
		return
	}
	if err := opener.ResetAssociation(mimeType); err != nil {
		m.setError("Could not reset %s: %v", mimeType, err)
		return
	}
	m.showAssociations(d.Inputs[0].Value(), d.AssocMine, mimeType)
	m.setStatus("%s reset to the system's default", mimeType)
}

// --- the application chooser --------------------------------------------------------

// openAppChooser lets the user pick the application for mimeType, saved as
// its default: applications declaring the type first, the current default
// marked. With a target (a local path, or remote) the pick also opens it;
// without, the chooser was opened from the associations list, which it
// goes back to.
func (m *Model) openAppChooser(mimeType, target string, remote *remoteOpenTarget) {
	var fits, others []opener.App
	for _, a := range opener.ListApps() {
		if a.CanOpen(mimeType) {
			fits = append(fits, a)
		} else {
			others = append(others, a)
		}
	}
	all := append(fits, others...)
	if len(all) == 0 {
		m.setStatus("No application found to open %s with", mimeType)
		return
	}
	ti := textinput.New()
	ti.Placeholder = "type to filter"
	ti.CharLimit = 100
	ti.SetWidth(40)
	ti.Focus()
	d := Dialog{
		Kind: DialogChooseApp, Inputs: []textinput.Model{ti},
		ChooseAppMime: mimeType, ChooseAppTarget: target, ChooseAppRemote: remote,
		ChooseAppAll: all, ChooseAppFits: len(fits),
		ChooseAppSetOnly: target == "" && remote == nil,
	}
	if cur, ok := opener.DefaultApp(mimeType); ok {
		d.ChooseAppCurrent = cur.ID
	}
	if d.ChooseAppSetOnly {
		d.Title = "Application for " + mimeType
	} else {
		name := filepath.Base(target)
		if remote != nil {
			name = remote.name
		}
		d.Title = "Open " + name + " with…"
	}
	m.dialog = d
	m.filterAppChooser()
}

// filterAppChooser rebuilds the chooser's visible list from its filter,
// keeping the cursor on the same application when it's still shown.
func (m *Model) filterAppChooser() {
	d := &m.dialog
	selected := ""
	if d.ItemIdx >= 0 && d.ItemIdx < len(d.ChooseApps) {
		selected = d.ChooseApps[d.ItemIdx].ID
	} else if len(d.ChooseApps) == 0 {
		selected = d.ChooseAppCurrent
	}
	q := strings.ToLower(strings.TrimSpace(d.Inputs[0].Value()))
	d.ChooseApps, d.Items, d.ChooseAppDim = nil, nil, nil
	d.ItemIdx = 0
	for i, a := range d.ChooseAppAll {
		if q != "" && !strings.Contains(strings.ToLower(a.Name), q) &&
			!strings.Contains(strings.ToLower(a.ID), q) {
			continue
		}
		label := a.Name
		if a.ID == d.ChooseAppCurrent {
			label += "  ✓ current"
		}
		if a.ID == selected {
			d.ItemIdx = len(d.Items)
		}
		d.ChooseApps = append(d.ChooseApps, a)
		d.Items = append(d.Items, label)
		d.ChooseAppDim = append(d.ChooseAppDim, i >= d.ChooseAppFits)
	}
	d.ListTop = scrollListTop(0, d.ItemIdx, m.dialogListRows(), len(d.Items))
}

// updateChooseAppKey handles the chooser's keys: arrows move, Enter picks,
// Esc goes back; anything else edits the filter.
func (m *Model) updateChooseAppKey(msg tea.KeyMsg) tea.Cmd {
	d := &m.dialog
	switch msg.String() {
	case "esc":
		if d.ChooseAppSetOnly {
			m.showAssociations(d.AssocQuery, d.AssocMine, d.ChooseAppMime)
		} else {
			m.dialog = Dialog{}
		}
		return nil
	case "enter":
		m.pickChosenApp()
		return nil
	}
	if m.moveListCursor(msg.String()) {
		return nil
	}
	before := d.Inputs[0].Value()
	var cmd tea.Cmd
	d.Inputs[0], cmd = d.Inputs[0].Update(msg)
	if d.Inputs[0].Value() != before {
		d.ItemIdx = -1
		m.filterAppChooser()
	}
	return cmd
}

// pickChosenApp saves the highlighted application as the type's default,
// then opens the file with it, or goes back to the associations list.
func (m *Model) pickChosenApp() {
	d := m.dialog
	if d.ItemIdx < 0 || d.ItemIdx >= len(d.ChooseApps) {
		return
	}
	app := d.ChooseApps[d.ItemIdx]
	saveErr := opener.SaveDefaultApp(d.ChooseAppMime, app)
	if d.ChooseAppSetOnly {
		m.showAssociations(d.AssocQuery, d.AssocMine, d.ChooseAppMime)
		if saveErr != nil {
			m.setError("Could not save the association: %v", saveErr)
		} else {
			m.setStatus("%s now opens with %s", d.ChooseAppMime, app.Name)
		}
		return
	}
	m.dialog = Dialog{}
	if d.ChooseAppRemote != nil {
		m.startOpenRemote(d.ChooseAppRemote, app)
		return
	}
	if err := opener.Launch(app, d.ChooseAppTarget); err != nil {
		m.setError("Could not launch %s: %v", app.Name, err)
		return
	}
	if saveErr != nil {
		m.setStatus("Opened with %s (not remembered: %v)", app.Name, saveErr)
		return
	}
	m.setStatus("Opened with %s (remembered as default for %s)", app.Name, d.ChooseAppMime)
}

// --- rendering ----------------------------------------------------------------------

func (m *Model) renderAssociations(b *strings.Builder) string {
	d := &m.dialog
	shown := "all file types"
	if d.AssocMine {
		shown = "only the types you chose an application for"
	}
	b.WriteString(styleDim.Render("Showing " + shown + " (Tab switches)."))
	b.WriteString("\n\nFilter: " + d.Inputs[0].View() + "\n\n")
	if len(d.Items) == 0 {
		b.WriteString("\n" + styleDim.Render("  nothing matches: type an extension (e.g. .md) to add it") + "\n")
	} else {
		renderScrollList(b, d.Items, d.AssocSystem, d.ItemIdx, d.ListTop, m.dialogListRows(), 80)
	}
	b.WriteString("\n" + styleDim.Render("Enter change application · Del reset to the system's · Esc close"))
	return dialogBox(84).Render(b.String())
}

func (m *Model) renderChooseApp(b *strings.Builder) string {
	d := &m.dialog
	switch {
	case d.ChooseAppSetOnly:
		b.WriteString(styleDim.Render(truncate("Default application for "+d.ChooseAppMime+":", 60)))
	case d.ChooseAppCurrent == "":
		b.WriteString(styleDim.Render(truncate("No default application is set for "+d.ChooseAppMime+".", 60)))
	default:
		b.WriteString(styleDim.Render(truncate("Open with, and use from now on for "+d.ChooseAppMime+":", 60)))
	}
	b.WriteString("\n\nFilter: " + d.Inputs[0].View() + "\n\n")
	if len(d.Items) == 0 {
		b.WriteString("\n" + styleDim.Render("  no application matches") + "\n")
	} else {
		renderScrollList(b, d.Items, d.ChooseAppDim, d.ItemIdx, d.ListTop, m.dialogListRows(), 60)
	}
	hint := "↑/↓ move · Enter choose · Esc cancel"
	if d.ChooseAppSetOnly {
		hint = "↑/↓ move · Enter set as default · Esc back"
	}
	b.WriteString("\n" + styleDim.Render("Dimmed: not declared for this type.\n"+hint))
	return dialogBox(64).Render(b.String())
}
