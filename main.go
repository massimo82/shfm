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

// Command shfm: an interactive terminal file manager, single- or dual-pane,
// with full mouse support (including drag&drop), keyboard shortcuts, multi-
// selection, a Freedesktop.org Trash Specification-compliant trash, and
// browsing of local drives, removable media, MTP devices, SMB shares, NFS
// exports and SFTP servers.
package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/applog"
	"shfm/internal/config"
	"shfm/internal/desktopfile"
	"shfm/internal/ui"
)

func main() {
	cfg := config.Load()

	// Best-effort, same reasoning as the desktop-launcher check below: shfm's
	// own stdout/stderr belong to the TUI (see ui.Model.View), so
	// diagnostics worth keeping go to their own log file instead — see
	// internal/applog's doc comment. A failure here just means Debug/Info/...
	// calls elsewhere silently do nothing, not a reason to abort startup.
	if err := applog.Init(applog.ParseLevel(cfg.LogLevel)); err != nil {
		fmt.Fprintln(os.Stderr, "note: could not open shfm log file:", err)
	}
	defer applog.Close()

	// Best-effort, silent unless it fails in a way worth knowing about:
	// install a .desktop launcher entry on first run only (skipped
	// entirely if one already exists system-wide or for this user).
	if err := desktopfile.EnsureInstalled(); err != nil {
		fmt.Fprintln(os.Stderr, "note: could not install desktop launcher entry:", err)
	}

	keymap := config.LoadKeyMap()

	// One optional positional argument: a folder to open both panes on
	// instead of the home folder (e.g. `shfm /mnt/data`). Anything invalid
	// (missing, not a directory, unresolvable) is silently ignored by
	// ui.New/resolveStartPath in favor of the usual home-folder default,
	// rather than refusing to start over a typo'd path.
	var startPath string
	if len(os.Args) > 1 {
		startPath = os.Args[1]
	}
	m := ui.New(cfg, keymap, startPath)

	// Alternate screen and mouse reporting are requested by the model's
	// View (bubbletea v2 has no program options for them).
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
