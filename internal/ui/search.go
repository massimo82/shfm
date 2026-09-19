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
	"regexp"
	"strings"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"shfm/internal/vfs"
)

// searchMatcher returns a match function for query, honoring regexMode
// (Go regexp, case-insensitive) over a plain substring match
// (case-insensitive, matching anywhere in the name — the same "type to
// narrow the list" behaviour as every other file manager, and what
// regexMode already does implicitly since Go's regexp isn't anchored by
// default); compiled once so callers can reuse it across many names
// instead of recompiling per entry. An error is returned only for an
// invalid regex pattern.
func searchMatcher(query string, regexMode bool) (func(name string) bool, error) {
	if query == "" {
		return func(string) bool { return true }, nil
	}
	if !regexMode {
		lower := strings.ToLower(query)
		return func(name string) bool { return strings.Contains(strings.ToLower(name), lower) }, nil
	}
	re, err := regexp.Compile("(?i)" + query)
	if err != nil {
		return nil, err
	}
	return re.MatchString, nil
}

// --- dialog -----------------------------------------------------------------------

// openSearch opens the search/filter dialog, pre-filled with whatever
// live filter is already active on the pane (if any) so re-opening it to
// tweak a query doesn't lose it.
func (m *Model) openSearch() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	d := newSingleInputDialog(DialogSearch, "Search / filter", "text or regex…", p.FilterQuery)
	d.SearchRegex = p.FilterRegex
	m.dialog = d
}

// updateSearchDialogKey handles keyboard input while the search dialog is
// open: Tab toggles exact/regex matching, Ctrl+R toggles current-folder vs.
// recursive scope, Enter applies the filter (current folder) or starts a
// background recursive search, Esc cancels back to whatever filter (if
// any) was active before the dialog was opened. Every other key goes to
// the query text input, live-updating the current-folder filter as you
// type (recursive search only runs once, on Enter — re-walking a remote
// tree on every keystroke would be wasteful and slow).
func (m *Model) updateSearchDialogKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	d := &m.dialog
	p := m.activePane()
	switch msg.String() {
	case "esc":
		m.dialog = Dialog{}
		return nil, true

	case "tab":
		d.SearchRegex = !d.SearchRegex
		if !d.SearchRecursive {
			m.applyLiveFilter()
		}
		return nil, false

	case "ctrl+r":
		d.SearchRecursive = !d.SearchRecursive
		if !d.SearchRecursive {
			m.applyLiveFilter()
		}
		return nil, false

	case "enter":
		query := strings.TrimSpace(d.Inputs[0].Value())
		if d.SearchRecursive {
			regexMode := d.SearchRegex
			m.dialog = Dialog{}
			if query == "" {
				p.ClearFilter()
				return nil, true
			}
			m.startRecursiveSearch(query, regexMode)
			return nil, true
		}
		m.dialog = Dialog{}
		return nil, true
	}

	var cmd tea.Cmd
	d.Inputs[0], cmd = d.Inputs[0].Update(msg)
	if !d.SearchRecursive {
		m.applyLiveFilter()
	}
	return cmd, false
}

// applyLiveFilter applies the search dialog's current query/mode to the
// active pane as a live, current-folder filter.
func (m *Model) applyLiveFilter() {
	d := &m.dialog
	p := m.activePane()
	query := strings.TrimSpace(d.Inputs[0].Value())
	if query == "" {
		p.ClearFilter()
		return
	}
	p.SetFilter(query, d.SearchRegex)
}

// --- recursive search ---------------------------------------------------------------

// maxSearchResults/maxSearchDirs bound a recursive search's cost and
// memory use against a huge (or, via a directory symlink cycle, unbounded)
// tree, particularly over a slow remote source.
const (
	maxSearchResults = 1000
	maxSearchDirs    = 5000
)

// searchResultMsg carries the outcome of a background recursive search
// (see startRecursiveSearch).
type searchResultMsg struct {
	requestID int
	paneIdx   int
	entries   []vfs.Entry
	truncated bool
	cancelled bool
	err       error
}

// paneSearchState tracks one pane's in-flight recursive search, if any, so
// a bare Esc (no dialog open) cancels the right pane's search and a stale
// result from a superseded search on THAT pane is discarded — independent
// of whatever the other pane's own search is doing.
type paneSearchState struct {
	running bool
	cancel  *int32
	reqID   int
}

func (m *Model) waitForSearchMsg() tea.Cmd {
	return func() tea.Msg {
		return <-m.searchCh
	}
}

// startRecursiveSearch walks the active pane's current folder downward in
// the background, collecting entries matching query (honoring regexMode),
// and shows the results once done — never blocking the UI, and cancellable
// (Esc) since a slow remote tree could otherwise run for a very long time.
func (m *Model) startRecursiveSearch(query string, regexMode bool) {
	match, err := searchMatcher(query, regexMode)
	if err != nil {
		m.setError("Invalid regex: %v", err)
		return
	}
	paneIdx := m.active
	p := m.panes[paneIdx]
	id := m.nextSearchID
	m.nextSearchID++
	var cancelled int32
	m.searchState[paneIdx] = paneSearchState{running: true, cancel: &cancelled, reqID: id}
	fs, startPath := p.FS, p.Path
	ch := m.searchCh
	m.setStatus("Searching … (Esc to cancel)")
	go func() {
		results, truncated := walkSearch(fs, startPath, match, func() bool {
			return atomic.LoadInt32(&cancelled) == 1
		})
		if atomic.LoadInt32(&cancelled) == 1 {
			ch <- searchResultMsg{requestID: id, paneIdx: paneIdx, cancelled: true}
			return
		}
		ch <- searchResultMsg{requestID: id, paneIdx: paneIdx, entries: results, truncated: truncated}
	}()
}

// cancelSearch requests cancellation of the active pane's in-flight
// recursive search, if any; the goroutine notices at the next directory
// boundary and reports back via searchResultMsg{cancelled: true}.
func (m *Model) cancelSearch() {
	st := m.searchState[m.active]
	if st.cancel != nil {
		atomic.StoreInt32(st.cancel, 1)
	}
}

func (m *Model) handleSearchResultMsg(msg searchResultMsg) {
	if msg.paneIdx < 0 || msg.paneIdx >= len(m.panes) {
		return
	}
	st := &m.searchState[msg.paneIdx]
	if msg.requestID != st.reqID {
		return // superseded by a newer search on this same pane
	}
	*st = paneSearchState{}
	if msg.cancelled {
		m.setStatus("Search cancelled")
		return
	}
	if msg.err != nil {
		m.setError("Search failed: %v", msg.err)
		return
	}
	p := m.panes[msg.paneIdx]
	p.ShowSearchResults(msg.entries)
	switch {
	case len(msg.entries) == 0:
		m.setStatus("No matches")
	case msg.truncated:
		m.setStatus("%d matches (showing first %d)", len(msg.entries), maxSearchResults)
	default:
		m.setStatus("%d match(es)", len(msg.entries))
	}
}

// walkSearch recursively lists folders from startPath downward (within fs),
// collecting entries whose name matches, up to maxSearchResults, checking
// cancelled between every directory so a stuck/huge remote tree can be
// aborted. A matching folder is included as a result AND still descended
// into. Symlinked directories are listed but never descended into, exactly
// like a plain `find` (no -L): the symlink-resolution added elsewhere in
// shfm (SFTP/NFS) makes IsDir correct for a directory symlink, but
// following it here could walk into a cycle and never terminate.
func walkSearch(fs vfs.FileSystem, startPath string, match func(string) bool, cancelled func() bool) (results []vfs.Entry, truncated bool) {
	dirsVisited := 0
	var walk func(dir, relPrefix string) bool // false = stop (truncated or cancelled)
	walk = func(dir, relPrefix string) bool {
		if cancelled() {
			return false
		}
		dirsVisited++
		if dirsVisited > maxSearchDirs {
			return false
		}
		entries, err := fs.List(dir)
		if err != nil {
			return true
		}
		for _, e := range entries {
			if cancelled() {
				return false
			}
			relName := relPrefix + e.Name
			if match(e.Name) {
				hit := e
				hit.Name = relName
				results = append(results, hit)
				if len(results) >= maxSearchResults {
					return false
				}
			}
			if e.IsDir && !e.IsSymlink {
				if !walk(fs.Join(dir, e.Name), relName+"/") {
					return false
				}
			}
		}
		return true
	}
	if !walk(startPath, "") {
		if !cancelled() {
			truncated = true
		}
	}
	sortEntriesDirsFirst(results)
	return results, truncated
}
