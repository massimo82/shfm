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
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"shfm/internal/version"
	"shfm/internal/vfs"
)

func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 || m.height == 0 {
		return "Loading…"
	}

	title := styleTitle.Width(m.width).Render(m.titleText())
	body := m.renderBody()
	help := styleHelpLine.Width(m.width).Render(" " + truncate(paneHelpHints, m.width-2))
	status := m.renderStatus()

	screen := lipgloss.JoinVertical(lipgloss.Left, title, body, help, status)

	if m.dialog.Kind != DialogNone {
		box := m.renderDialogBox()
		bw, bh := lipgloss.Width(box), lipgloss.Height(box)
		x0 := maxInt((m.width-bw)/2, 0)
		y0 := maxInt((m.height-bh)/2, 0)
		m.dialogRect = rect{x0: x0, y0: y0, w: bw, h: bh}
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}
	return screen
}

func (m *Model) titleText() string {
	mode := "Single pane"
	if m.dualPane {
		mode = "Dual-pane"
	}
	clip := ""
	if !m.clipboard.Empty() {
		clip = fmt.Sprintf("  ·  %d ready to paste", len(m.clipboard.Names))
	}
	bg := ""
	if n := m.runningTaskCount(); n > 0 {
		bg = fmt.Sprintf("  ·  %d background task(s) — Ctrl+B", n)
	}
	return fmt.Sprintf("  shfm — Shell File Manager v%s  ·  %s (click to toggle)%s%s", version.Version, mode, clip, bg)
}

func (m *Model) runningTaskCount() int {
	n := 0
	for _, t := range m.tasks {
		if !t.Finished {
			n++
		}
	}
	return n
}

func (m *Model) renderBody() string {
	h := m.bodyHeight()
	if m.dualPane {
		leftW := m.width / 2
		rightW := m.width - leftW
		left := m.renderPane(0, leftW, h)
		right := m.renderPane(1, rightW, h)
		return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	}
	return m.renderPane(m.active, m.width, h)
}

// padRight pads/truncates s to exactly w cells, used to build raw text
// lines before styling, so the SOURCE/PATH fields and their buttons stay
// aligned.
func padRight(s string, w int) string {
	r := []rune(s)
	if len(r) >= w {
		if w <= 0 {
			return ""
		}
		return string(r[:w])
	}
	return s + strings.Repeat(" ", w-len(r))
}

func (m *Model) renderPane(idx, width, height int) string {
	p := m.panes[idx]
	g := computePaneGeom(0, 0, width, height)
	active := idx == m.active

	var lines []string
	lines = append(lines, m.renderSourceRow(p, g))
	lines = append(lines, m.renderPathRow(p, g))

	if p.Err != nil {
		errLine := padRight(" error: "+p.Err.Error(), g.innerW)
		listLines := []string{styleErr.Render(errLine)}
		for len(listLines) < g.listH {
			listLines = append(listLines, strings.Repeat(" ", g.innerW))
		}
		lines = append(lines, listLines...)
	} else if p.Mode == PaneTrash {
		lines = append(lines, m.renderTrashLines(p, g.innerW, g.listH)...)
	} else {
		lines = append(lines, m.renderEntryLines(idx, p, g.innerW, g.listH)...)
	}

	lines = append(lines, m.renderDetailRow(p, g.innerW))

	for len(lines) < g.innerH {
		lines = append(lines, strings.Repeat(" ", g.innerW))
	}
	if len(lines) > g.innerH {
		lines = lines[:g.innerH]
	}
	content := strings.Join(lines, "\n")

	style := stylePaneBorder
	if active {
		style = stylePaneActive
	}
	return style.Width(g.innerW).Height(g.innerH).Render(content)
}

func (m *Model) renderSourceRow(p *Pane, g paneGeom) string {
	label := styleFieldLabel.Render(padRight("SOURCE", g.labelW))
	value := p.SourceLabel
	if p.Mode == PaneTrash {
		value = "Trash (" + p.SourceLabel + ")"
	}
	box := styleFieldBox.Render(padRight(" "+value, g.sourceBoxW))
	btn := " " + styleFieldButton.Render(padRight(sourceButtonText, g.sourceBtnW))
	trashBtn := " " + styleFieldButton.Render(padRight(trashButtonText, g.sourceTrashW))
	return label + box + btn + trashBtn
}

func (m *Model) renderPathRow(p *Pane, g paneGeom) string {
	label := styleFieldLabel.Render(padRight("PATH", g.labelW))
	var box string
	if p.PathEditing {
		p.PathInput.Width = maxInt(g.pathBoxW-3, 1)
		box = styleFieldBoxEdit.Render(padRight(" "+p.PathInput.View(), g.pathBoxW))
	} else {
		box = styleFieldBox.Render(padRight(" "+p.Path, g.pathBoxW))
	}
	btn := " " + styleFieldButton.Render(padRight(pathButtonText, g.pathBtnW))
	newBtn := " " + styleFieldButton.Render(padRight(newButtonText, g.pathNewW))
	return label + box + btn + newBtn
}

// renderDetailRow shows the permissions (rwx for user/group/others, plus
// any immutable/append-only chattr flag), owner, group, and
// modification/creation dates of whichever entry is currently
// under the pane's cursor — a file, folder, or symlink. It always sits at
// the bottom of the file list, changes as the cursor moves, and is blank
// when there's nothing meaningful to show (empty list, the ".." entry, an
// error, or the trash view).
func (m *Model) renderDetailRow(p *Pane, w int) string {
	text := detailLineText(p)
	return styleFieldBox.Render(padRight(truncate(text, w), w))
}

// detailLineText builds the raw (unstyled) text for renderDetailRow; split
// out so it can be tested directly without stripping ANSI styling codes.
func detailLineText(p *Pane) string {
	if p.Mode != PaneNormal || p.Err != nil {
		return ""
	}
	e, ok := p.CurrentEntry()
	if !ok || IsParentEntry(e) {
		return ""
	}

	perm := permString(e.Mode)
	// A chattr flag (immutable/append-only) that not even root can override
	// goes right after the permissions, where a narrow pane's truncation
	// won't cut it off; it only ever appears on the rare entry that has one.
	if ar, ok := p.FS.(vfs.AttrReader); ok {
		if attrs := ar.Attributes(p.FS.Join(p.Path, e.Name)); len(attrs) > 0 {
			perm += " [" + strings.Join(attrs, ",") + "]"
		}
	}
	owner := firstNonEmpty(e.Owner, "-")
	group := firstNonEmpty(e.Group, "-")

	modified := "-"
	if !e.ModTime.IsZero() {
		modified = e.ModTime.Format("2006-01-02 15:04")
	}

	created := "-"
	if bt, ok := p.FS.(vfs.BirthTimer); ok {
		// Cheap (a single syscall, not a directory walk) and only ever
		// attempted for backends that support it (currently: local
		// filesystem only) — never a blocking network round-trip on
		// SMB/NFS/MTP/SFTP, where BirthTimer simply isn't implemented.
		if t, err := bt.BirthTime(p.FS.Join(p.Path, e.Name)); err == nil && !t.IsZero() {
			created = t.Format("2006-01-02 15:04")
		}
	}

	return fmt.Sprintf(" %s  %s:%s  M:%s  C:%s", perm, owner, group, modified, created)
}

// permString renders the classic 9-character "rwxr-xr-x" permission string
// for user/group/others from a file mode.
func permString(mode os.FileMode) string {
	const letters = "rwxrwxrwx"
	perm := mode.Perm()
	b := []byte(letters)
	for i := 0; i < 9; i++ {
		if perm&(1<<uint(8-i)) == 0 {
			b[i] = '-'
		}
	}
	return string(b)
}

// --- entry coloring by type and permissions ---------------------------------

var archiveExt = map[string]bool{".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".bz2": true, ".xz": true, ".7z": true, ".rar": true, ".zst": true}
var imageExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true, ".svg": true, ".webp": true, ".ico": true, ".tiff": true}
var mediaExt = map[string]bool{".mp3": true, ".mp4": true, ".mkv": true, ".avi": true, ".wav": true, ".flac": true, ".mov": true, ".ogg": true, ".webm": true, ".m4a": true}

func extOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return strings.ToLower(name[i:])
	}
	return ""
}

// entryIconAndStyle picks a pseudo-icon and a color for e based on its
// type (standard XDG user dir/folder/symlink/executable/archive/image/
// media/plain file) and, as a modifier, its permissions (entries with no
// write permission at all are shown in a fainter variant of their color, as
// a visual cue). isXDGDir marks e as one of the user's standard XDG user
// directories (Desktop, Documents, ...) — see Pane.XDGNames.
func entryIconAndStyle(e vfs.Entry, isXDGDir bool) (string, lipgloss.Style) {
	var icon string
	var style lipgloss.Style
	switch {
	case IsParentEntry(e):
		return iconDir, styleDim
	case isXDGDir:
		icon, style = iconDir, styleXDGDir
	case e.IsDir:
		icon, style = iconDir, styleDir
	case e.IsSymlink:
		icon, style = iconSymlink, styleSymlink
	case e.Mode&0o111 != 0:
		icon, style = iconFile, styleExec
	case archiveExt[extOf(e.Name)]:
		icon, style = iconFile, styleArchive
	case imageExt[extOf(e.Name)]:
		icon, style = iconFile, styleImage
	case mediaExt[extOf(e.Name)]:
		icon, style = iconFile, styleMedia
	default:
		icon, style = iconFile, styleFile
	}
	if e.Mode != 0 && e.Mode&0o222 == 0 {
		style = style.Faint(true)
	}
	return icon, style
}

func (m *Model) renderEntryLines(paneIdx int, p *Pane, w, h int) []string {
	var out []string
	if len(p.Entries) == 0 {
		out = append(out, styleDim.Width(w).Render(" (empty folder)"))
	}
	end := p.Offset + h
	if end > len(p.Entries) {
		end = len(p.Entries)
	}
	for i := p.Offset; i < end; i++ {
		e := p.Entries[i]
		icon, nameStyle := entryIconAndStyle(e, p.XDGNames[e.Name])
		mark := " "
		if p.Selected[e.Name] {
			mark = "\u2713"
			nameStyle = styleSelected
		}
		sizeStr := humanSize(e.Size)
		countStr := ""
		switch {
		case IsParentEntry(e):
			sizeStr = ""
		case e.IsDir && !p.DirSizesSupported:
			// This source can't compute recursive folder sizes/counts at
			// all (e.g. SMB/NFS/MTP/SFTP: walking a remote subtree just to
			// show them would be too slow) — leave both blank rather than
			// showing a meaningless "0B"/"0".
			sizeStr = ""
		case e.IsDir && e.Size == SizePending:
			// Still being computed in the background (see pane.go/
			// dirsize.go): shown as soon as the result arrives.
			sizeStr = "?B"
			countStr = "?"
		case e.IsDir:
			countStr = humanCount(e.ItemCount)
		}
		nameW := w - (5 + len(icon) + 8 + 7)
		if nameW < 1 {
			nameW = 1
		}
		name := e.Name
		if e.IsDir && !IsParentEntry(e) {
			name += "/"
		}
		row := fmt.Sprintf("%s %s %-*s %6s %8s", mark, icon, nameW, truncate(name, nameW), countStr, sizeStr)
		cell := nameStyle
		if i == p.Cursor {
			cell = cell.Background(colCursorBg)
		}
		if m.drag.active && m.drag.hoverPane == paneIdx && m.drag.hoverIdx == i {
			cell = cell.Background(colAccent)
		}
		out = append(out, cell.Width(w).Render(row))
	}
	return out
}

func (m *Model) renderTrashLines(p *Pane, w, h int) []string {
	var out []string
	if len(p.TrashItems) == 0 {
		out = append(out, styleDim.Width(w).Render(" (trash is empty)"))
	}
	end := p.Offset + h
	if end > len(p.TrashItems) {
		end = len(p.TrashItems)
	}
	for i := p.Offset; i < end; i++ {
		it := p.TrashItems[i]
		icon := iconFile
		if it.IsDir {
			icon = iconDir
		}
		nameW := w - (5 + len(icon) + 17)
		if nameW < 1 {
			nameW = 1
		}
		date := it.DeletionDate.Format("2006-01-02 15:04")
		row := fmt.Sprintf("  %s %-*s %s", icon, nameW, truncate(it.OriginalPath, nameW), date)
		cell := styleFile
		if i == p.Cursor {
			cell = cell.Background(colCursorBg)
		}
		out = append(out, cell.Width(w).Render(row))
	}
	return out
}

func (m *Model) renderStatus() string {
	left := m.status
	if left == "" {
		left = "Ready."
	}
	style := styleStatus
	if m.statusErr {
		style = style.Foreground(colErr)
	}
	return style.Width(m.width).Render(" " + truncate(left, m.width-2))
}

// --- progress bar -------------------------------------------------------------

func renderProgressBar(width int, done, total int) string {
	if width < 4 {
		width = 4
	}
	frac := 0.0
	if total > 0 {
		frac = float64(done) / float64(total)
	}
	filled := int(frac * float64(width))
	if filled > width {
		filled = width
	}
	bar := styleProgressFill.Render(strings.Repeat(" ", filled)) +
		styleProgressEmpty.Render(strings.Repeat(" ", width-filled))
	return bar
}
