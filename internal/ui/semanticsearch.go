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
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"shfm/internal/semantic"
	"shfm/internal/vfs"
)

// openSemanticSearch is the Ctrl+F entry point: "semantic" search over
// file *contents* (TXT/PDF/DOCX text, semantically matched), as opposed to
// "/"'s search over file *names*. Deliberately local-only (see
// internal/semantic's doc comment on why indexing remote content isn't
// attempted) and lazy: nothing is indexed until this is used for the first
// time on a given folder, and only that folder's subtree is indexed — never
// the whole disk.
func (m *Model) openSemanticSearch() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	if _, ok := p.FS.(*vfs.LocalFS); !ok {
		m.setStatus("Semantic (content) search only works on local folders")
		return
	}
	if !semantic.Available {
		m.setError("%v", semantic.ErrUnavailable)
		return
	}
	if m.semanticIndexing[p.Path] {
		m.setStatus("Still indexing %s for semantic search…", p.Path)
		return
	}
	if m.semanticEngine.HasIndex(p.Path) {
		// Open the dialog immediately — no perceptible delay for what's by
		// far the common case (an already-indexed, unchanged folder) — and
		// kick off a staleness check (cheap: stat only, see EnsureIndex) in
		// the background in case files changed since the last index. If it
		// finds and re-embeds actual changes, that briefly races with the
		// query the user is about to type; an update landing a few hundred
		// ms late is a fair trade against making every reopen wait on it.
		m.dialog = newSingleInputDialog(DialogSemanticSearch, "Semantic search (content)", "what are you looking for…", "")
		m.startSemanticIndex(p.Path, true)
		return
	}
	m.startSemanticIndex(p.Path, false)
}

// startSemanticIndex builds or refreshes the content index for root in the
// background, never blocking the UI: browsing (or, for a refresh, using the
// search dialog) continues normally while it runs.
func (m *Model) startSemanticIndex(root string, refresh bool) {
	m.semanticIndexing[root] = true
	if !refresh {
		m.setStatus("Building content index for semantic search in %s… (first time only; keep browsing, you'll be notified)", root)
	}

	statusCh := make(chan semantic.Status, 16)
	ch := m.semanticCh
	go m.semanticEngine.EnsureIndex(context.Background(), root, statusCh)
	go func() {
		for st := range statusCh {
			ch <- semanticMsg{kind: semanticIndexProgress, root: root, indexStatus: st, refresh: refresh}
		}
	}()
}

// startSemanticQuery runs a semantic search against root's already-built
// index in the background: even though it should normally be fast, nothing
// involving model inference gets to block the UI, on principle.
func (m *Model) startSemanticQuery(root, query string) {
	ch := m.semanticCh
	engine := m.semanticEngine
	m.setStatus("Searching …")
	go func() {
		results, err := engine.Search(context.Background(), root, query, 200)
		ch <- semanticMsg{kind: semanticQueryDone, root: root, results: results, err: err}
	}()
}

// semanticMsgKind distinguishes the two things startSemanticIndex/
// startSemanticQuery report back over the same channel.
type semanticMsgKind int

const (
	semanticIndexProgress semanticMsgKind = iota
	semanticQueryDone
)

type semanticMsg struct {
	kind        semanticMsgKind
	root        string
	indexStatus semantic.Status // semanticIndexProgress
	results     []semantic.Result
	err         error
	refresh     bool // semanticIndexProgress: a background staleness check, not a first-time build
}

func (m *Model) waitForSemanticMsg() tea.Cmd {
	return func() tea.Msg {
		return <-m.semanticCh
	}
}

func (m *Model) handleSemanticMsg(msg semanticMsg) {
	switch msg.kind {
	case semanticIndexProgress:
		if !msg.indexStatus.Finished {
			// A background refresh runs silently: the search dialog (or
			// results) may already be on screen, and "Indexing…" spam
			// while the user is mid-query would just be noise for what's
			// normally a near-instant stat-only check anyway.
			if !msg.refresh {
				m.setStatus("Indexing %s for semantic search: %d/%d…", msg.root, msg.indexStatus.Done, msg.indexStatus.Total)
			}
			return
		}
		delete(m.semanticIndexing, msg.root)
		if msg.indexStatus.Err != nil {
			// Same reasoning: don't interrupt the user over a background
			// refresh failing — the previously-indexed data is still there
			// and still usable, which is strictly better than an error
			// popup for something they didn't explicitly ask for.
			if !msg.refresh {
				m.setError("Semantic search index for %s failed: %v", msg.root, msg.indexStatus.Err)
			}
			return
		}
		if !msg.refresh {
			m.setStatus("Semantic search ready for %s — press Ctrl+F again to search", msg.root)
		}

	case semanticQueryDone:
		if msg.err != nil {
			m.setError("Semantic search failed: %v", msg.err)
			return
		}
		m.showSemanticResults(msg.root, msg.results)
	}
}

// showSemanticResults finds the pane still showing root (the search may
// have taken long enough that the user switched away) and displays the
// matches exactly like a recursive name search does: each result's RelPath
// becomes an Entry.Name relative to root, so navigation, opening and
// multi-select copy/move/delete on the results work with no further
// special-casing (see search.go's ShowSearchResults for why this works).
func (m *Model) showSemanticResults(root string, results []semantic.Result) {
	for _, p := range m.panes {
		if p.Path != root {
			continue
		}
		entries := make([]vfs.Entry, 0, len(results))
		for _, r := range results {
			e := vfs.Entry{Name: r.RelPath}
			if info, err := p.FS.Stat(p.FS.Join(root, r.RelPath)); err == nil {
				e = info
				e.Name = r.RelPath
			}
			entries = append(entries, e)
		}
		p.ShowSearchResults(entries)
		if len(entries) == 0 {
			m.setStatus("No content matches")
		} else {
			m.setStatus("%d content match(es)", len(entries))
		}
		return
	}
}

// updateSemanticSearchDialogKey handles the (single-field) semantic
// search dialog: Enter runs the query, Esc cancels.
func (m *Model) updateSemanticSearchDialogKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	d := &m.dialog
	switch msg.String() {
	case "esc":
		m.dialog = Dialog{}
		return nil, true
	case "enter":
		query := strings.TrimSpace(d.Inputs[0].Value())
		root := m.activePane().Path
		m.dialog = Dialog{}
		if query == "" {
			return nil, true
		}
		m.startSemanticQuery(root, query)
		return nil, true
	}
	var cmd tea.Cmd
	d.Inputs[0], cmd = d.Inputs[0].Update(msg)
	return cmd, false
}
