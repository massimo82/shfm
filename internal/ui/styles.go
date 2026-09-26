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

import "charm.land/lipgloss/v2"

var (
	colBorder    = lipgloss.Color("240")
	colBorderAct = lipgloss.Color("62")
	colDir       = lipgloss.Color("39")
	colXDGDir    = lipgloss.Color("141") // standard XDG user dirs (Desktop, Documents, ...) in $HOME
	colSymlink   = lipgloss.Color("214")
	colFile      = lipgloss.Color("252")
	colExec      = lipgloss.Color("40")  // executable files
	colArchive   = lipgloss.Color("203") // zip/tar/gz/...
	colImage     = lipgloss.Color("176") // png/jpg/...
	colMedia     = lipgloss.Color("80")  // mp3/mp4/...
	colSelected  = lipgloss.Color("205")
	colCursorBg  = lipgloss.Color("237")
	colHeaderFg  = lipgloss.Color("230")
	colHeaderBg  = lipgloss.Color("62")
	colStatusFg  = lipgloss.Color("230")
	colStatusBg  = lipgloss.Color("236")
	colErr       = lipgloss.Color("196")
	colWarn      = lipgloss.Color("214")
	colOK        = lipgloss.Color("42")
	colDim       = lipgloss.Color("244")
	colAccent    = lipgloss.Color("212")
	colFieldBg   = lipgloss.Color("235")
)

var (
	styleTitle = lipgloss.NewStyle().Foreground(colHeaderFg).Background(colHeaderBg).Bold(true).Padding(0, 1)

	stylePaneBorder = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colBorder)
	stylePaneActive = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colBorderAct)

	stylePathBar = lipgloss.NewStyle().Foreground(colHeaderFg).Background(colHeaderBg).Bold(true)

	styleDir      = lipgloss.NewStyle().Foreground(colDir).Bold(true)
	styleXDGDir   = lipgloss.NewStyle().Foreground(colXDGDir).Bold(true)
	styleSymlink  = lipgloss.NewStyle().Foreground(colSymlink)
	styleFile     = lipgloss.NewStyle().Foreground(colFile)
	styleExec     = lipgloss.NewStyle().Foreground(colExec).Bold(true)
	styleArchive  = lipgloss.NewStyle().Foreground(colArchive)
	styleImage    = lipgloss.NewStyle().Foreground(colImage)
	styleMedia    = lipgloss.NewStyle().Foreground(colMedia)
	styleSelected = lipgloss.NewStyle().Foreground(colSelected).Bold(true)
	styleCursor   = lipgloss.NewStyle().Background(colCursorBg)
	styleDim      = lipgloss.NewStyle().Foreground(colDim)
	styleErr      = lipgloss.NewStyle().Foreground(colErr).Bold(true)
	styleWarn     = lipgloss.NewStyle().Foreground(colWarn).Bold(true)
	styleOK       = lipgloss.NewStyle().Foreground(colOK)
	styleAccent   = lipgloss.NewStyle().Foreground(colAccent).Bold(true)

	styleStatus = lipgloss.NewStyle().Foreground(colStatusFg).Background(colStatusBg).Padding(0, 1)

	styleDialogBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(1, 2)

	styleHelpKey  = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleHelpDesc = lipgloss.NewStyle().Foreground(colFile)

	// Field row styles (SOURCE/PATH boxes and their buttons), per the
	// mockup layout: label, box (display), box (editing), button.
	styleFieldLabel   = lipgloss.NewStyle().Foreground(colDim).Bold(true)
	styleFieldBox     = lipgloss.NewStyle().Foreground(colFile).Background(colFieldBg)
	styleFieldBoxEdit = lipgloss.NewStyle().Foreground(colHeaderFg).Background(colCursorBg)
	styleFieldButton  = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	styleHelpLine     = lipgloss.NewStyle().Foreground(colDim).Background(colFieldBg)

	// Progress bar.
	styleProgressFill  = lipgloss.NewStyle().Background(colOK)
	styleProgressEmpty = lipgloss.NewStyle().Background(colFieldBg)
)

const (
	iconDir     = "[D]"
	iconFile    = "[F]"
	iconSymlink = "[L]"
	iconTrash   = "[T]"

	iconSourceLocal     = "[HDD]"
	iconSourceRemovable = "[USB]"
	iconSourceSMB       = "[SMB]"
	iconSourceNFS       = "[NFS]"
	iconSourceSFTP      = "[SFTP]"
	iconSourceMTP       = "[MTP]"
	iconSourceAdd       = "[ + ]"
	iconSourceFormat    = "[FMT]"
)

// paneHelpHints is the compact reminder line shown once, below both panes,
// even in dual-pane mode. No function keys (F1-F12): every shortcut goes
// through Ctrl (and Ctrl+Alt for the "alternative" variants).
const paneHelpHints = "Ctrl+C copy · Ctrl+V paste · Ctrl+Alt+V paste&move · Ctrl+D trash · Ctrl+Alt+D delete · Ctrl+P path · Ctrl+S source · Ctrl+L layout · Ctrl+B tasks · Ctrl+Alt+H help · Ctrl+Up/Down move cursor · Left/Right switch pane"
