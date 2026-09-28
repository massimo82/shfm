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
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/archive"
	"shfm/internal/fileops"
)

// --- extract ---------------------------------------------------------------------

// doExtract extracts the selected archives (or the one under the cursor),
// each into a new folder next to it named after it minus its extension,
// as a background task. Selected entries that aren't archives are left
// alone.
func (m *Model) doExtract() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	var items []fileops.Item
	others := 0
	for _, n := range p.SelectedNames() {
		if e, ok := p.entryByName(n); ok && !e.IsDir && archive.IsArchive(n) {
			items = append(items, fileops.Item{FS: p.FS, Path: p.FS.Join(p.Path, n)})
		} else {
			others++
		}
	}
	if len(items) == 0 {
		m.setStatus("Nothing to extract: not an archive")
		return
	}
	p.DeselectAll()
	t := m.startTask(TaskExtract, len(items), func(prog *fileops.Progress) *fileops.Result {
		return fileops.Extract(items, prog)
	})
	m.dialog = Dialog{Kind: DialogProgress, Title: t.Kind.String(), TaskID: t.ID}
	if others > 0 {
		m.setStatus("%d selected item(s) aren't archives and were left out", others)
	}
}

// --- create ----------------------------------------------------------------------

// askCreateArchive opens the dialog choosing the format and name of a new
// archive of the selected entries (or the one under the cursor), created
// in the same folder.
func (m *Model) askCreateArchive() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	names := p.SelectedNames()
	if len(names) == 0 {
		m.setStatus("Nothing to archive")
		return
	}
	kinds := archive.Creatable()
	idx := 0
	for i, k := range kinds {
		if k == m.archiveKind {
			idx = i
		}
	}
	labels := make([]string, len(kinds))
	for i, k := range kinds {
		labels[i] = k.Label()
	}
	d := newSingleInputDialog(DialogCreateArchive, "Create archive", "archive name", defaultArchiveBase(p, names)+kinds[idx].Ext())
	d.Items, d.ItemIdx, d.ArchiveKinds, d.ArchiveNames = labels, idx, kinds, names
	m.dialog = d
}

// defaultArchiveBase proposes a new archive's name, before its extension:
// the entry's own name (a file's minus its extension) for a single entry,
// the folder's name for several.
func defaultArchiveBase(p *Pane, names []string) string {
	if len(names) == 1 {
		n := names[0]
		if e, ok := p.entryByName(n); ok && !e.IsDir {
			if kind, base := archive.Detect(n); kind != "" {
				return base
			}
			if ext := filepath.Ext(n); ext != "" && ext != n {
				return strings.TrimSuffix(n, ext)
			}
		}
		return n
	}
	if base := p.FS.Base(p.Path); base != "" && base != "/" && base != "." {
		return base
	}
	return "archive"
}

// updateCreateArchiveKey handles the keys that pick the format — every
// other key edits the name; handled reports whether msg was one of them.
func (m *Model) updateCreateArchiveKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	d := &m.dialog
	n := len(d.ArchiveKinds)
	switch msg.String() {
	case "up", "shift+tab":
		m.selectArchiveKind((d.ItemIdx - 1 + n) % n)
	case "down", "tab":
		m.selectArchiveKind((d.ItemIdx + 1) % n)
	default:
		return nil, false
	}
	return nil, true
}

// selectArchiveKind picks format i, swapping the name's extension for
// the new format's.
func (m *Model) selectArchiveKind(i int) {
	d := &m.dialog
	name := d.Inputs[0].Value()
	if _, base := archive.Detect(name); archive.IsArchive(name) {
		name = base
	}
	d.ItemIdx = i
	d.Message = ""
	d.Inputs[0].SetValue(name + d.ArchiveKinds[i].Ext())
	d.Inputs[0].CursorEnd()
}

// performCreateArchive starts creating the archive the dialog describes,
// as a background task. The format's extension is added to a name typed
// without it; an existing name is refused, never overwritten.
func (m *Model) performCreateArchive() {
	d := &m.dialog
	p := m.activePane()
	kind := d.ArchiveKinds[d.ItemIdx]
	name := strings.TrimSpace(d.Inputs[0].Value())
	if !strings.HasSuffix(strings.ToLower(name), kind.Ext()) {
		name += kind.Ext()
	}
	switch {
	case name == kind.Ext():
		d.Message = "Type a name for the archive"
		return
	case strings.Contains(name, "/"):
		d.Message = "The name can't contain \"/\""
		return
	}
	dest := p.FS.Join(p.Path, name)
	if _, err := p.FS.Stat(dest); err == nil {
		d.Message = name + " already exists"
		return
	}
	items := make([]fileops.Item, len(d.ArchiveNames))
	for i, n := range d.ArchiveNames {
		items[i] = fileops.Item{FS: p.FS, Path: p.FS.Join(p.Path, n)}
	}
	m.archiveKind = kind
	p.DeselectAll()
	fs := p.FS
	t := m.startTask(TaskCompress, len(items), func(prog *fileops.Progress) *fileops.Result {
		return fileops.CreateArchive(items, fs, dest, kind, prog)
	})
	t.Label = name
	m.dialog = Dialog{Kind: DialogProgress, Title: t.Kind.String(), TaskID: t.ID}
}
