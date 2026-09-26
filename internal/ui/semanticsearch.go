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
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/applog"
	"shfm/internal/notify"
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
		m.startSemanticIndex(p.FS, p.Path, true)
		return
	}
	m.startSemanticIndex(p.FS, p.Path, false)
}

// startSemanticIndex builds or refreshes the content index for root in the
// background, never blocking the UI: browsing (or, for a refresh, using the
// search dialog) continues normally while it runs.
func (m *Model) startSemanticIndex(fs vfs.FileSystem, root string, refresh bool) {
	m.semanticIndexing[root] = true
	if !refresh {
		m.setStatus("Building content index for semantic search in %s… (first time only; keep browsing, you'll be notified)", root)
	}

	statusCh := make(chan semantic.Status, 16)
	ch := m.semanticCh
	engine := m.semanticEngine
	go func() {
		// dirSize/start are only worth the trouble for a first-time build
		// (refresh's own staleness check stays silent, same as elsewhere in
		// this file) and are computed here, in this already-background
		// goroutine, rather than in startSemanticIndex itself — a DirSize
		// walk is stat-only (no file content read) so it's normally fast,
		// but on a huge subtree it's still a full recursive walk, and nothing
		// here may block the bubbletea event loop that called us.
		var dirSize int64
		if !refresh {
			if sizer, ok := fs.(vfs.DirSizer); ok {
				if sz, _, err := sizer.DirSize(root); err == nil {
					dirSize = sz
				}
			}
		}
		// Timed from here, not from startSemanticIndex's own call: this
		// excludes the DirSize walk above, so "indexing time" reflects the
		// embedding work itself.
		start := time.Now()
		go engine.EnsureIndex(context.Background(), root, statusCh)
		for st := range statusCh {
			msg := semanticMsg{kind: semanticIndexProgress, root: root, indexStatus: st, refresh: refresh}
			if st.Finished && !refresh {
				msg.elapsed = time.Since(start)
				msg.dirSize = dirSize
			}
			ch <- msg
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
	refresh     bool          // semanticIndexProgress: a background staleness check, not a first-time build
	elapsed     time.Duration // semanticIndexProgress, finished, !refresh: total time spent indexing
	dirSize     int64         // semanticIndexProgress, finished, !refresh: root's size (bytes) before indexing
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
			m.setStatus("Semantic search ready for %s (%s indexed in %s) — press Ctrl+F again to search",
				msg.root, humanSize(msg.dirSize), formatMinutes(msg.elapsed))
			// The status line above is only seen if/when the user happens
			// to glance back at it; a first-time build is exactly the slow
			// case ("keep browsing, you'll be notified" — see
			// startSemanticIndex) they're expected to wander off during, so
			// send an actual desktop notification too, but only if they're
			// not already sitting right there watching this same folder
			// (in either pane) — in which case the status line update above
			// is enough, same reasoning as a file task's own progress
			// dialog being on screen (see notifyTaskFinished).
			if !m.isPathActive(msg.root) {
				m.notifySemanticIndexDone(msg.root, msg.dirSize, msg.elapsed)
			}
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

// isPathActive reports whether path is the folder either pane currently has
// open — i.e. the user is right there, as opposed to having navigated
// elsewhere while a first-time semantic index build ran in the background.
func (m *Model) isPathActive(path string) bool {
	for _, p := range m.panes {
		if p.Path == path {
			return true
		}
	}
	return false
}

// formatMinutes renders a duration as minutes, the unit the user actually
// cares about here (an index build worth waiting for runs from seconds to
// several minutes) — one decimal, e.g. "0.2 min" for a quick folder,
// "3.4 min" for a large one.
func formatMinutes(d time.Duration) string {
	return fmt.Sprintf("%.1f min", d.Minutes())
}

// notifySemanticIndexDone posts a desktop notification (see internal/notify)
// once a first-time semantic index build finishes while the user has
// navigated away from root — the case the in-app status line alone won't
// reach. Mirrors notifyTaskFinished: skipped if the user opted out via
// config, fire-and-forget otherwise so a slow/absent session bus never
// blocks the UI, and any failure is only logged.
func (m *Model) notifySemanticIndexDone(root string, dirSize int64, elapsed time.Duration) {
	if m.cfg != nil && !m.cfg.Notifications {
		return
	}
	summary := "shfm: semantic search index ready"
	body := fmt.Sprintf("%s — %s indexed in %s", root, humanSize(dirSize), formatMinutes(elapsed))
	go func() {
		if err := notify.Send(summary, body, notify.Normal); err != nil {
			applog.Debug("desktop notification failed", "error", err)
		}
	}()
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
