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
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const doubleClickWindow = 400 * time.Millisecond

func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.dialog.Kind != DialogNone {
		m.handleDialogMouse(msg)
		return m, nil
	}

	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if msg.Action == tea.MouseActionPress {
			if hit := m.hitTest(msg.X, msg.Y); hit.pane >= 0 {
				m.active = hit.pane
			}
			m.activePane().MoveCursor(-3, m.listHeight())
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if msg.Action == tea.MouseActionPress {
			if hit := m.hitTest(msg.X, msg.Y); hit.pane >= 0 {
				m.active = hit.pane
			}
			m.activePane().MoveCursor(3, m.listHeight())
		}
		return m, nil
	}

	if msg.Button != tea.MouseButtonLeft && msg.Action != tea.MouseActionRelease {
		return m, nil
	}

	switch msg.Action {
	case tea.MouseActionPress:
		m.handleMousePress(msg)
	case tea.MouseActionMotion:
		if m.drag.active {
			if msg.X != m.drag.startX || msg.Y != m.drag.startY {
				m.drag.moved = true
			}
			hit := m.hitTest(msg.X, msg.Y)
			if hit.pane >= 0 {
				m.drag.hoverPane = hit.pane
				if hit.zone == zoneListRow {
					m.drag.hoverIdx = hit.entryIdx
				} else {
					m.drag.hoverIdx = -1
				}
			}
		}
	case tea.MouseActionRelease:
		if m.drag.active {
			if m.drag.moved {
				m.performDrop(msg)
			} else {
				m.handleSimpleClick(msg)
			}
		}
		m.drag = dragState{}
	}
	return m, nil
}

func (m *Model) handleMousePress(msg tea.MouseMsg) {
	hit := m.hitTest(msg.X, msg.Y)

	if hit.zone == zoneTitleBar {
		m.toggleLayout()
		return
	}
	if hit.zone == zoneHelpLine {
		m.openHelp()
		return
	}
	if hit.pane < 0 {
		return
	}
	m.active = hit.pane
	p := m.panes[hit.pane]

	switch hit.zone {
	case zoneSourceBox, zoneSourceButton:
		m.openSourceMenu()
	case zoneSourceTrashButton:
		m.toggleTrashView()
	case zonePathButton:
		if p.Mode == PaneNormal {
			p.GoUp()
		}
	case zonePathNewButton:
		if p.Mode == PaneNormal {
			m.openNewItemChoice()
		}
	case zonePathBox:
		if p.Mode == PaneNormal {
			p.BeginPathEdit()
		}
	case zoneListRow:
		switch {
		case msg.Ctrl:
			p.Cursor = hit.entryIdx
			p.ToggleSelectName(p.Entries[hit.entryIdx].Name)
			p.RangeAnchor = hit.entryIdx
		case msg.Shift:
			p.Cursor = hit.entryIdx
			p.SelectRange(p.RangeAnchor, hit.entryIdx)
		default:
			p.Cursor = hit.entryIdx
			p.RangeAnchor = hit.entryIdx
			m.drag = dragState{
				active: true, pane: hit.pane, startIdx: hit.entryIdx,
				startX: msg.X, startY: msg.Y, startTime: time.Now(),
				fromDir: p.Path, hoverPane: hit.pane, hoverIdx: hit.entryIdx,
			}
			if e, ok := p.CurrentEntry(); ok && !IsParentEntry(e) {
				if len(p.Selected) > 0 && p.Selected[e.Name] {
					m.drag.names = p.SelectedNames()
				} else {
					m.drag.names = []string{e.Name}
				}
			}
		}
		p.fixOffset(m.listHeight())
	}
}

// handleSimpleClick handles a "plain" click (no drag) on a list row,
// recognizing a double click to enter a folder, go up via "..", restore a
// trash item, or open a file with its default application.
func (m *Model) handleSimpleClick(msg tea.MouseMsg) {
	hit := m.hitTest(msg.X, msg.Y)
	if hit.zone != zoneListRow {
		return
	}
	now := time.Now()
	isDouble := m.lastClick.pane == hit.pane && m.lastClick.idx == hit.entryIdx &&
		now.Sub(m.lastClick.t) < doubleClickWindow
	m.lastClick = clickMemo{pane: hit.pane, idx: hit.entryIdx, t: now}
	if isDouble {
		m.enterOrOpen()
	}
}

// performDrop finishes a drag&drop: moves (or copies, if Ctrl is held on
// release) the dragged items into the destination folder, which can be in
// the same pane or the other one, even on a different source (local/
// removable/MTP/SMB/NFS/SFTP).
func (m *Model) performDrop(msg tea.MouseMsg) {
	hit := m.hitTest(msg.X, msg.Y)
	if hit.pane < 0 || hit.zone == zoneSourceBox || hit.zone == zoneSourceButton ||
		hit.zone == zoneSourceTrashButton || hit.zone == zonePathBox ||
		hit.zone == zonePathButton || hit.zone == zonePathNewButton {
		m.setStatus("Drag cancelled")
		return
	}
	destPane := m.panes[hit.pane]
	destDir := destPane.Path
	if hit.zone == zoneListRow {
		if e := destPane.Entries[hit.entryIdx]; e.IsDir && !IsParentEntry(e) {
			destDir = destPane.FS.Join(destPane.Path, e.Name)
		}
	}
	srcPane := m.panes[m.drag.pane]
	if len(m.drag.names) == 0 {
		return
	}
	if destDir == m.drag.fromDir && destPane.FS == srcPane.FS {
		m.setStatus("Drag cancelled (same folder)")
		return
	}
	m.startTransfer(srcPane.FS, m.drag.fromDir, m.drag.names, destPane.FS, destDir, msg.Ctrl)
	srcPane.DeselectAll()
}

// handleDialogMouse handles mouse clicks while a dialog is open: clicking a
// source-menu/task-list/etc. item selects it immediately; clicking a form
// field (SMB/NFS/SFTP connect, properties) focuses it. The coordinate math
// mirrors renderDialogBox's construction exactly (title + blank line, then
// the body line by line).
func (m *Model) handleDialogMouse(msg tea.MouseMsg) {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		return
	}
	r := m.dialogRect
	if r.w == 0 {
		return
	}
	contentTop := r.y0 + 2 + 2  // border(1)+padding(1) + title(1)+blank(1)
	contentLeft := r.x0 + 1 + 2 // border(1)+padding(2)
	contentRight := r.x0 + r.w - 1 - 2

	if msg.Y < contentTop || msg.X < contentLeft || msg.X >= contentRight {
		return
	}
	row := msg.Y - contentTop

	switch m.dialog.Kind {
	case DialogSourceMenu:
		// Unlike the other list dialogs below, this one has a blank
		// separator line between groups (local disks/removable/MTP/remote/
		// "new connection"), so a rendered row doesn't map 1:1 to an item
		// index — sourceMenuRows (shared with renderDialogBox) gives the
		// row each entry actually landed on; clicking a separator's row
		// simply matches nothing.
		for i, r := range sourceMenuRows(m.sourceMenuEntries) {
			if r == row {
				m.dialog.ItemIdx = i
				m.confirmDialog()
				break
			}
		}
	case DialogTaskList, DialogNewChoice, DialogChooseApp, DialogFormatChoose:
		if row >= 0 && row < len(m.dialog.Items) {
			m.dialog.ItemIdx = row
			m.confirmDialog()
		}
	case DialogHelp:
		m.dialog = Dialog{}
	case DialogConnectSMB, DialogConnectNFS, DialogConnectSFTP, DialogProperties:
		if row >= 0 && row < len(m.dialog.Inputs) {
			m.dialog.Inputs[m.dialog.FocusIdx].Blur()
			m.dialog.FocusIdx = row
			m.dialog.Inputs[row].Focus()
		}
	}
}
