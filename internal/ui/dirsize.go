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

import tea "github.com/charmbracelet/bubbletea"

// SizePending is the sentinel Entry.Size value meaning "this folder's
// recursive size is being computed in the background, not shown yet".
// Real sizes are always >= 0, so -1 is safe to use as a marker.
const SizePending int64 = -1

// dirSizeMsg carries the result of computing one folder's recursive size
// and item count, sent from a background goroutine spawned by Pane.Load
// (see pane.go) to the main bubbletea event loop.
type dirSizeMsg struct {
	paneIndex int
	dirPath   string // the folder listing this result belongs to
	name      string // the child folder's name within dirPath
	size      int64
	itemCount int64
}

// waitForSizeMsg returns a tea.Cmd that blocks on the size-result channel
// and delivers the next one as a message — the same long-poll pattern used
// for background file-operation tasks (see tasks.go).
func (m *Model) waitForSizeMsg() tea.Cmd {
	return func() tea.Msg {
		return <-m.sizeCh
	}
}

// handleDirSizeMsg applies a computed folder size to the matching pane's
// entry, but only if that pane is still showing the very folder listing
// the result was computed for — otherwise (the user navigated away while
// the computation was running) the result is simply discarded, since it no
// longer applies to anything currently on screen.
func (m *Model) handleDirSizeMsg(msg dirSizeMsg) {
	if msg.paneIndex < 0 || msg.paneIndex >= len(m.panes) {
		return
	}
	p := m.panes[msg.paneIndex]
	if p.Mode != PaneNormal || p.Path != msg.dirPath {
		return
	}
	for i := range p.Entries {
		if p.Entries[i].Name == msg.name {
			p.Entries[i].Size = msg.size
			p.Entries[i].ItemCount = msg.itemCount
			break
		}
	}
}
