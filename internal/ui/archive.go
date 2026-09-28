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

	tea "charm.land/bubbletea/v2"

	"shfm/internal/archive"
	"shfm/internal/fileops"
)

// --- extract ---------------------------------------------------------------------

// archiveReadNeeds and archiveCreateNeeds say what to install to read or
// write a format ("" when nothing): variables, so tests can simulate a
// machine without some tool.
var (
	archiveReadNeeds   = archive.ReadNeeds
	archiveCreateNeeds = archive.CreateNeeds
)

// doExtract extracts the selected archives (or the one under the cursor),
// each into a new folder next to it named after it minus its extension,
// as a background task. Selected entries that aren't archives are left
// alone, and so are archives this machine lacks the tool to read, saying
// what to install.
func (m *Model) doExtract() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	var items []fileops.Item
	others := 0
	var unreadable []string // names of archives needing a missing tool
	need := ""              // what the last of them needs
	for _, n := range p.SelectedNames() {
		e, ok := p.entryByName(n)
		kind, _ := archive.Detect(n)
		switch {
		case !ok || e.IsDir || kind == "":
			others++
		case archiveReadNeeds(kind) != "":
			unreadable = append(unreadable, n)
			need = archiveReadNeeds(kind)
		default:
			items = append(items, fileops.Item{FS: p.FS, Path: p.FS.Join(p.Path, n)})
		}
	}
	if len(items) == 0 {
		switch {
		case len(unreadable) == 1:
			m.setError("Can't extract %s: install %s", unreadable[0], need)
		case len(unreadable) > 1:
			m.setError("Can't extract %d archives: a tool is missing (%s needs %s)", len(unreadable), unreadable[len(unreadable)-1], need)
		default:
			m.setStatus("Nothing to extract: not an archive")
		}
		return
	}
	p.DeselectAll()
	t := m.startTask(TaskExtract, len(items), func(prog *fileops.Progress) *fileops.Result {
		return fileops.Extract(items, prog)
	})
	m.dialog = Dialog{Kind: DialogProgress, Title: t.Kind.String(), TaskID: t.ID}
	switch {
	case len(unreadable) > 0:
		m.setStatus("%d archive(s) left out: a tool is missing (%s needs %s)", len(unreadable), unreadable[len(unreadable)-1], need)
	case others > 0:
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
	// Every format is listed; the ones this machine can't write are shown
	// disabled, with what to install.
	kinds := archive.CreateKinds()
	labels := make([]string, len(kinds))
	needs := make([]string, len(kinds))
	idx := -1
	for i, k := range kinds {
		labels[i], needs[i] = k.Label(), archiveCreateNeeds(k)
		if needs[i] == "" && (idx < 0 || k == m.archiveKind) {
			idx = i
		}
	}
	if idx < 0 {
		m.setError("No archive format can be created")
		return
	}
	d := newSingleInputDialog(DialogCreateArchive, "Create archive", "archive name", defaultArchiveBase(p, names)+kinds[idx].Ext())
	d.Items, d.ItemIdx, d.ArchiveKinds, d.ArchiveNeeds, d.ArchiveNames = labels, idx, kinds, needs, names
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
	step := 1
	switch msg.String() {
	case "up", "shift+tab":
		step = -1
	case "down", "tab":
	default:
		return nil, false
	}
	// Disabled formats are skipped.
	for i := (d.ItemIdx + step + n) % n; i != d.ItemIdx; i = (i + step + n) % n {
		if d.ArchiveNeeds[i] == "" {
			m.selectArchiveKind(i)
			break
		}
	}
	return nil, true
}

// selectArchiveKind picks format i, swapping the name's extension for
// the new format's; a format this machine can't write only says what to
// install.
func (m *Model) selectArchiveKind(i int) {
	d := &m.dialog
	if need := d.ArchiveNeeds[i]; need != "" {
		d.Message = fmt.Sprintf("Install %s to create %s archives", need, d.ArchiveKinds[i].Ext())
		return
	}
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
