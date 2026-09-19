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

// paneGeom describes the computed geometry of one pane, in absolute
// terminal coordinates: computed once per pane and reused by both the
// renderer (view.go) and the mouse hit-test (this file), so the two always
// stay pixel-for-pixel consistent.
type paneGeom struct {
	x0, y0, w, h   int
	innerX, innerY int
	innerW, innerH int
	labelW         int
	sourceRowY     int
	pathRowY       int
	listTopY       int
	listH          int
	detailRowY     int

	sourceBoxX0   int
	sourceBoxW    int
	sourceBtnX0   int // "[...]" — open the source picker
	sourceBtnW    int
	sourceTrashX0 int // "[T]" — toggle the trash view
	sourceTrashW  int

	pathBoxX0 int
	pathBoxW  int
	pathBtnX0 int // "[..]" — go up one folder
	pathBtnW  int
	pathNewX0 int // "[+]" — new file/folder
	pathNewW  int
}

const (
	fieldLabelWidth  = 7 // "SOURCE " / "PATH   "
	sourceButtonText = "[...]"
	trashButtonText  = "[T]"
	pathButtonText   = "[..]"
	newButtonText    = "[+]"
)

// computePaneGeom computes the geometry of a pane of size w×h with its top
// left corner at (x0,y0), per the layout: border, SOURCE row (label + field
// + "[...]" + "[T]"), PATH row (label + field + "[..]" + "[+]"), file
// list, a per-pane DETAIL row (permissions/owner/group/dates of whichever
// entry is under the cursor), border. The summary help line is NOT part of
// the pane: it's unique and shared, drawn once below both panes even in
// dual-pane mode (see renderBody in view.go).
func computePaneGeom(x0, y0, w, h int) paneGeom {
	g := paneGeom{x0: x0, y0: y0, w: w, h: h}
	g.innerX, g.innerY = x0+1, y0+1
	g.innerW = maxInt(w-2, 4)
	g.innerH = maxInt(h-2, 5)

	g.sourceRowY = g.innerY
	g.pathRowY = g.innerY + 1
	g.listTopY = g.innerY + 2
	g.listH = maxInt(g.innerH-3, 1)
	g.detailRowY = g.listTopY + g.listH

	g.labelW = fieldLabelWidth
	gap := 1

	sBtnW := len([]rune(sourceButtonText))
	tBtnW := len([]rune(trashButtonText))
	g.sourceBtnW = sBtnW
	g.sourceTrashW = tBtnW
	g.sourceBoxW = maxInt(g.innerW-g.labelW-2*gap-sBtnW-tBtnW, 1)
	g.sourceBoxX0 = g.innerX + g.labelW
	g.sourceBtnX0 = g.sourceBoxX0 + g.sourceBoxW + gap
	g.sourceTrashX0 = g.sourceBtnX0 + sBtnW + gap

	pBtnW := len([]rune(pathButtonText))
	nBtnW := len([]rune(newButtonText))
	g.pathBtnW = pBtnW
	g.pathNewW = nBtnW
	g.pathBoxW = maxInt(g.innerW-g.labelW-2*gap-pBtnW-nBtnW, 1)
	g.pathBoxX0 = g.innerX + g.labelW
	g.pathBtnX0 = g.pathBoxX0 + g.pathBoxW + gap
	g.pathNewX0 = g.pathBtnX0 + pBtnW + gap

	return g
}

func (m *Model) bodyHeight() int {
	// title(1) + shared help line(1) + status bar(1)
	h := m.height - 3
	if h < 1 {
		h = 1
	}
	return h
}

// shownPanes returns the indices of the panes actually visible: both in
// dual-pane mode, only the active one in single-pane mode.
func (m *Model) shownPanes() []int {
	if m.dualPane {
		return []int{0, 1}
	}
	return []int{m.active}
}

// paneRect returns the rectangle (absolute terminal coordinates) occupied
// by pane idx. Body rows start right after the title bar (row 0).
func (m *Model) paneRect(idx int) (x0, y0, w, h int) {
	y0 = 1
	h = m.bodyHeight()
	if m.dualPane {
		leftW := m.width / 2
		if idx == 0 {
			return 0, y0, leftW, h
		}
		return leftW, y0, m.width - leftW, h
	}
	return 0, y0, m.width, h
}

func (m *Model) paneGeom(idx int) paneGeom {
	x0, y0, w, h := m.paneRect(idx)
	return computePaneGeom(x0, y0, w, h)
}

// zoneKind identifies which interactive element of the pane a mouse click
// landed on.
type zoneKind int

const (
	zoneNone zoneKind = iota
	zoneSourceBox
	zoneSourceButton
	zoneSourceTrashButton
	zonePathBox
	zonePathButton
	zonePathNewButton
	zoneListRow
	zoneTitleBar
	zoneHelpLine
)

// hitResult is the outcome of resolving a mouse coordinate.
type hitResult struct {
	pane     int // -1 if the click isn't on any visible pane
	zone     zoneKind
	entryIdx int // valid only for zoneListRow
}

func inRange(v, from, width int) bool { return v >= from && v < from+width }

// hitTest converts a mouse coordinate (x,y) into the pane and interactive
// zone it falls on (source field, "[...]"/"[T]" buttons, path field,
// "[..]"/"[+]" buttons, a list row, the title bar...).
func (m *Model) hitTest(x, y int) hitResult {
	if y == 0 {
		return hitResult{pane: -1, zone: zoneTitleBar}
	}
	if y == m.height-2 {
		return hitResult{pane: -1, zone: zoneHelpLine}
	}
	for _, idx := range m.shownPanes() {
		g := m.paneGeom(idx)
		if x < g.x0 || x >= g.x0+g.w || y < g.y0 || y >= g.y0+g.h {
			continue
		}
		p := m.panes[idx]

		switch {
		case y == g.sourceRowY && inRange(x, g.sourceBoxX0, g.sourceBoxW):
			return hitResult{pane: idx, zone: zoneSourceBox}
		case y == g.sourceRowY && inRange(x, g.sourceBtnX0, g.sourceBtnW):
			return hitResult{pane: idx, zone: zoneSourceButton}
		case y == g.sourceRowY && inRange(x, g.sourceTrashX0, g.sourceTrashW):
			return hitResult{pane: idx, zone: zoneSourceTrashButton}
		case y == g.pathRowY && inRange(x, g.pathBoxX0, g.pathBoxW):
			return hitResult{pane: idx, zone: zonePathBox}
		case y == g.pathRowY && inRange(x, g.pathBtnX0, g.pathBtnW):
			return hitResult{pane: idx, zone: zonePathButton}
		case y == g.pathRowY && inRange(x, g.pathNewX0, g.pathNewW):
			return hitResult{pane: idx, zone: zonePathNewButton}
		case y >= g.listTopY && y < g.listTopY+g.listH:
			row := y - g.listTopY
			ei := p.Offset + row
			if ei >= 0 && ei < p.Len() {
				return hitResult{pane: idx, zone: zoneListRow, entryIdx: ei}
			}
			return hitResult{pane: idx, zone: zoneNone}
		default:
			return hitResult{pane: idx, zone: zoneNone}
		}
	}
	return hitResult{pane: -1}
}
