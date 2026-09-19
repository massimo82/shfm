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
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"

	"shfm/internal/trash"
	"shfm/internal/vfs"
)

var errNotADirectory = errors.New("the given path is not a folder")

// PaneMode distinguishes normal filesystem browsing from the trash view.
type PaneMode int

const (
	PaneNormal PaneMode = iota
	PaneTrash
)

// parentEntryName is the special name, always at the top of the listing
// when not already at the source's root, that lets the user go up a level
// by selecting it (Enter, a click or a double click), just like a normal
// ".." navigation entry.
const parentEntryName = ".."

// Pane holds the state of one panel (left or right).
type Pane struct {
	FS       vfs.FileSystem
	Path     string
	Entries  []vfs.Entry
	Cursor   int
	Offset   int // first visible row, for scrolling
	Selected map[string]bool

	Mode       PaneMode
	TrashItems []trash.Item

	// XDGNames holds the Entries names (only ever populated when Path is
	// the user's home folder on the local filesystem) that are the user's
	// standard XDG user directories (Desktop, Documents, ...) — set by
	// Load(), consulted by the renderer to list and color them separately
	// from the rest. Nil whenever none apply.
	XDGNames map[string]bool

	// Filter narrows Load()'s listing of the current folder to names
	// matching FilterQuery (exact or regex, see FilterRegex): see search.go.
	// Entries stays a plain []vfs.Entry either way, so selection and every
	// copy/move/delete/open action keep working unchanged.
	FilterQuery  string
	FilterRegex  bool
	FilterActive bool

	// ShowingSearchResults is set after a recursive search (see search.go)
	// replaces Entries with cross-folder matches, each Name carrying its
	// path relative to Path (e.g. "sub/dir/file.txt"). Every backend's
	// Join() just concatenates trimmed segments with "/", so an embedded
	// "/" in one Name resolves exactly like two separate elements would —
	// meaning navigation (Activate descends right into a matched folder),
	// opening and multi-select copy/move/delete on results work with no
	// further special-casing anywhere else. Esc clears it, restoring the
	// real listing of Path.
	ShowingSearchResults bool

	ShowHidden bool
	Err        error

	// SourceLabel is shown in the pane's SOURCE row.
	SourceLabel string

	// RangeAnchor is the index a range selection (Shift+click) starts from.
	RangeAnchor int

	// PathInput and PathEditing drive direct editing of the PATH field
	// (Ctrl+P or a click on the field): while PathEditing is true, keys are
	// routed to the text input instead of normal list navigation.
	PathInput   textinput.Model
	PathEditing bool

	// DirSizesSupported reports whether the active source can compute
	// recursive folder sizes at all (currently: local filesystem only);
	// used by the renderer to show a blank size for folders instead of a
	// misleading "0B" on sources where sizes are never computed.
	DirSizesSupported bool

	// index and sizeCh let Load() spawn a background goroutine that
	// computes folder sizes without blocking navigation, and tag its
	// results with which pane they belong to; set once by NewPane / when a
	// pane's source is replaced. sizeCh is nil-safe: Load() simply skips
	// background scanning if it hasn't been set (e.g. in tests).
	index  int
	sizeCh chan<- dirSizeMsg
}

// NewPane creates a panel on the given source/path. index (0=left, 1=right)
// and sizeCh (where to report asynchronously computed folder sizes) are
// used solely to drive the background folder-size scan — pass 0 and nil if
// that isn't needed (e.g. in tests).
func NewPane(fs vfs.FileSystem, path string, showHidden bool, index int, sizeCh chan<- dirSizeMsg) *Pane {
	p := &Pane{
		FS: fs, Path: path, Selected: map[string]bool{}, ShowHidden: showHidden,
		SourceLabel: fs.Label(), index: index, sizeCh: sizeCh,
	}
	p.Load()
	return p
}

// Load (re)reads the current folder's contents (or the trash's, in
// PaneTrash mode). When Path is the user's home folder, the standard XDG
// user directories (Desktop, Documents, ...) are listed first, alphabetically
// — see XDGNames and sortEntriesXDGFirst; everything else follows, folders
// before files, alphabetically. The special ".." entry is prepended when not
// at the root. For folders on a
// backend that implements vfs.DirSizer (currently only the local
// filesystem), their total recursive size AND item count (files and
// subfolders combined) are computed in a background goroutine and filled
// in asynchronously as results arrive (see dirSizeMsg): Load itself
// returns immediately, showing SizePending for those sizes in the
// meantime, so opening a folder never blocks on walking its subtrees.
func (p *Pane) Load() {
	p.Err = nil
	p.XDGNames = nil
	if p.Mode == PaneTrash {
		items, err := trash.List()
		if err != nil {
			p.Err = err
			p.TrashItems = nil
			return
		}
		sort.Slice(items, func(i, j int) bool { return items[i].DeletionDate.After(items[j].DeletionDate) })
		p.TrashItems = items
		if p.Cursor >= len(items) {
			p.Cursor = maxInt(0, len(items)-1)
		}
		return
	}

	p.ShowingSearchResults = false
	entries, err := p.FS.List(p.Path)
	if err != nil {
		p.Err = err
		p.Entries = nil
		return
	}
	match := func(string) bool { return true }
	if p.FilterActive {
		// An invalid in-progress regex (e.g. typing "foo[") falls back to
		// "show everything" rather than "show nothing" — much less jarring
		// while the query is mid-edit; see search.go's dialog handling,
		// which surfaces the actual compile error to the user.
		if m, err := searchMatcher(p.FilterQuery, p.FilterRegex); err == nil {
			match = m
		}
	}
	filtered := entries[:0:0]
	for _, e := range entries {
		if !p.ShowHidden && strings.HasPrefix(e.Name, ".") {
			continue
		}
		if !match(e.Name) {
			continue
		}
		filtered = append(filtered, e)
	}
	p.XDGNames = xdgEntryNames(p.FS, p.Path, filtered)
	sortEntriesXDGFirst(filtered, p.XDGNames)

	if sizer, ok := p.FS.(vfs.DirSizer); ok {
		p.DirSizesSupported = true
		var pendingNames []string
		for i := range filtered {
			if filtered[i].IsDir && !IsParentEntry(filtered[i]) {
				filtered[i].Size = SizePending
				pendingNames = append(pendingNames, filtered[i].Name)
			}
		}
		if len(pendingNames) > 0 && p.sizeCh != nil {
			// Compute recursive folder sizes in the background instead of
			// blocking here: on a folder with large subtrees, walking
			// every subfolder synchronously before showing anything would
			// make opening it (or even just starting the app, for $HOME)
			// noticeably slow. The listing above is already complete and
			// usable; sizes simply fill in progressively as they're ready
			// (see dirsize.go and Model's handling of dirSizeMsg).
			fs, dirPath, idx, ch := p.FS, p.Path, p.index, p.sizeCh
			go func() {
				for _, name := range pendingNames {
					sz, count, err := sizer.DirSize(fs.Join(dirPath, name))
					if err != nil {
						continue
					}
					ch <- dirSizeMsg{paneIndex: idx, dirPath: dirPath, name: name, size: sz, itemCount: count}
				}
			}()
		}
	} else {
		p.DirSizesSupported = false
	}

	if p.FS.Dir(p.Path) != p.Path {
		filtered = append([]vfs.Entry{{Name: parentEntryName, IsDir: true}}, filtered...)
	}
	p.Entries = filtered
	if p.Cursor >= len(p.Entries) {
		p.Cursor = maxInt(0, len(p.Entries)-1)
	}
	// Drop from the selection any name no longer present.
	for name := range p.Selected {
		found := false
		for _, e := range p.Entries {
			if e.Name == name {
				found = true
				break
			}
		}
		if !found {
			delete(p.Selected, name)
		}
	}
}

// sortEntriesDirsFirst sorts entries folders-before-files, then
// alphabetically (case-insensitive) — the ordering Load() uses for a normal
// listing and search.go reuses for recursive search results.
func sortEntriesDirsFirst(entries []vfs.Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

// sortEntriesXDGFirst sorts entries with the standard XDG user directories
// named in xdgNames first (alphabetically, case-insensitive), followed by
// everything else in sortEntriesDirsFirst's usual folders-before-files,
// alphabetical order. With a nil/empty xdgNames it behaves exactly like
// sortEntriesDirsFirst.
func sortEntriesXDGFirst(entries []vfs.Entry, xdgNames map[string]bool) {
	if len(xdgNames) == 0 {
		sortEntriesDirsFirst(entries)
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		xi, xj := xdgNames[entries[i].Name], xdgNames[entries[j].Name]
		if xi != xj {
			return xi
		}
		if xi {
			return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
		}
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

// SetFilter turns on (or updates) the current-folder live filter and
// reloads; regexMode selects Go regexp matching over a plain exact-name
// match. See search.go for searchMatcher.
func (p *Pane) SetFilter(query string, regexMode bool) {
	p.FilterQuery, p.FilterRegex, p.FilterActive = query, regexMode, true
	p.Load()
}

// ClearFilter turns off the live filter, if any, and reloads the full
// listing.
func (p *Pane) ClearFilter() {
	if !p.FilterActive {
		return
	}
	p.FilterQuery, p.FilterActive = "", false
	p.Load()
}

// ShowSearchResults replaces Entries with the results of a recursive search
// (see search.go), entirely bypassing Load()'s normal single-folder
// FS.List() — each result's Name already carries its path relative to Path.
func (p *Pane) ShowSearchResults(results []vfs.Entry) {
	p.Entries = results
	p.Cursor, p.Offset = 0, 0
	p.DeselectAll()
	p.ShowingSearchResults = true
	p.XDGNames = nil
}

// ExitSearchResults leaves search-results view, restoring the real listing
// of the current path.
func (p *Pane) ExitSearchResults() {
	if !p.ShowingSearchResults {
		return
	}
	p.Cursor, p.Offset = 0, 0
	p.Load()
}

func (p *Pane) Len() int {
	if p.Mode == PaneTrash {
		return len(p.TrashItems)
	}
	return len(p.Entries)
}

func (p *Pane) CurrentEntry() (vfs.Entry, bool) {
	if p.Mode == PaneTrash || p.Cursor < 0 || p.Cursor >= len(p.Entries) {
		return vfs.Entry{}, false
	}
	return p.Entries[p.Cursor], true
}

// IsParentEntry reports whether e is the special ".." entry.
func IsParentEntry(e vfs.Entry) bool { return e.Name == parentEntryName }

func (p *Pane) CurrentTrashItem() (trash.Item, bool) {
	if p.Mode != PaneTrash || p.Cursor < 0 || p.Cursor >= len(p.TrashItems) {
		return trash.Item{}, false
	}
	return p.TrashItems[p.Cursor], true
}

// SelectedNames returns the names currently selected with Space; if none
// are selected, returns (if present, and other than "..") just the entry
// under the cursor — this lets copy/move/delete/etc. work both on a multi-
// selection and on the single current entry, never accidentally including
// the ".." parent entry.
func (p *Pane) SelectedNames() []string {
	if len(p.Selected) > 0 {
		names := make([]string, 0, len(p.Selected))
		for n := range p.Selected {
			names = append(names, n)
		}
		sort.Strings(names)
		return names
	}
	if e, ok := p.CurrentEntry(); ok && !IsParentEntry(e) {
		return []string{e.Name}
	}
	return nil
}

func (p *Pane) ToggleSelectCurrent() {
	e, ok := p.CurrentEntry()
	if !ok || IsParentEntry(e) {
		return
	}
	if p.Selected[e.Name] {
		delete(p.Selected, e.Name)
	} else {
		p.Selected[e.Name] = true
	}
}

func (p *Pane) ToggleSelectName(name string) {
	if name == parentEntryName {
		return
	}
	if p.Selected[name] {
		delete(p.Selected, name)
	} else {
		p.Selected[name] = true
	}
}

func (p *Pane) SelectAll() {
	for _, e := range p.Entries {
		if !IsParentEntry(e) {
			p.Selected[e.Name] = true
		}
	}
}

func (p *Pane) DeselectAll() {
	p.Selected = map[string]bool{}
}

// SelectRange selects every entry between from and to (indices), inclusive,
// skipping the ".." entry if present.
func (p *Pane) SelectRange(from, to int) {
	if from > to {
		from, to = to, from
	}
	for i := from; i <= to && i < len(p.Entries); i++ {
		if i >= 0 && !IsParentEntry(p.Entries[i]) {
			p.Selected[p.Entries[i].Name] = true
		}
	}
}

func (p *Pane) MoveCursor(delta, visibleHeight int) {
	n := p.Len()
	if n == 0 {
		p.Cursor = 0
		p.Offset = 0
		return
	}
	p.Cursor += delta
	if p.Cursor < 0 {
		p.Cursor = 0
	}
	if p.Cursor >= n {
		p.Cursor = n - 1
	}
	p.fixOffset(visibleHeight)
}

func (p *Pane) fixOffset(visibleHeight int) {
	if visibleHeight <= 0 {
		return
	}
	if p.Cursor < p.Offset {
		p.Offset = p.Cursor
	}
	if p.Cursor >= p.Offset+visibleHeight {
		p.Offset = p.Cursor - visibleHeight + 1
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
}

// Activate performs the action bound to the entry under the cursor: goes up
// with GoUp if it's the special ".." entry, otherwise enters the folder (if
// it is one). Returns true if navigation happened.
func (p *Pane) Activate() bool {
	e, ok := p.CurrentEntry()
	if !ok {
		return false
	}
	if IsParentEntry(e) {
		return p.GoUp()
	}
	if !e.IsDir {
		return false
	}
	p.Path = p.FS.Join(p.Path, e.Name)
	p.Cursor, p.Offset = 0, 0
	p.DeselectAll()
	p.FilterQuery, p.FilterActive = "", false
	p.Load()
	return true
}

// GoUp goes up to the parent folder.
func (p *Pane) GoUp() bool {
	parent := p.FS.Dir(p.Path)
	if parent == p.Path {
		return false
	}
	prevBase := p.FS.Base(p.Path)
	p.Path = parent
	p.Cursor, p.Offset = 0, 0
	p.DeselectAll()
	p.FilterQuery, p.FilterActive = "", false
	p.Load()
	// Put the cursor back on the folder we came from, if possible.
	for i, e := range p.Entries {
		if e.Name == prevBase {
			p.Cursor = i
			break
		}
	}
	return true
}

// BeginPathEdit activates direct editing of the PATH field (Ctrl+P or a
// click on the field), pre-filled with the current path.
func (p *Pane) BeginPathEdit() {
	ti := textinput.New()
	ti.SetValue(p.Path)
	ti.CursorEnd()
	ti.Width = 60
	ti.Focus()
	p.PathInput = ti
	p.PathEditing = true
}

// CancelPathEdit leaves path-editing mode without applying any change.
func (p *Pane) CancelPathEdit() {
	p.PathEditing = false
}

// CommitPathEdit tries to navigate to the typed path: if it doesn't exist
// or isn't a folder, returns an error and stays in editing mode.
func (p *Pane) CommitPathEdit() error {
	target := strings.TrimSpace(p.PathInput.Value())
	if target == "" {
		p.PathEditing = false
		return nil
	}
	entry, err := p.FS.Stat(target)
	if err != nil {
		return err
	}
	if !entry.IsDir {
		return errNotADirectory
	}
	p.Path = target
	p.Cursor, p.Offset = 0, 0
	p.DeselectAll()
	p.FilterQuery, p.FilterActive = "", false
	p.Load()
	p.PathEditing = false
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
