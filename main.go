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
	"shfm/internal/filemanager1"
	"shfm/internal/fusemount"
	"shfm/internal/pick"
	"shfm/internal/polkitagent"
	"shfm/internal/portal"
	"shfm/internal/ui"
	"shfm/internal/vfs"
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

	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "shfm:", err)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch {
	case opts.help:
		fmt.Print(usage)
		return
	case opts.fileManager1:
		serveOrExit(filemanager1.Serve(func(args []string) error {
			return startShfmInTerminal(cfg, args)
		}, serviceIdleExit))
		return
	case opts.portal:
		serveOrExit(portal.Serve(func(sock string) (<-chan error, error) {
			return startShfmInTerminalWait(cfg, []string{"--pick", sock})
		}, serviceIdleExit))
		return
	}

	// File chooser mode: the portal backend that started this shfm (see
	// internal/portal) waits on the socket for the request, and the
	// answer.
	var session *pick.Session
	if opts.pickSocket != "" {
		s, req, err := pick.Dial(opts.pickSocket)
		if err != nil {
			fmt.Fprintln(os.Stderr, "shfm: could not reach the file chooser portal:", err)
			os.Exit(1)
		}
		session = s
		opts.start.Pick = &req
	}

	m := ui.New(cfg, keymap, opts.start)

	// Sources are exposed to external apps through FUSE mounts, made as
	// soon as a network source is opened (other sources: on first use);
	// they must go away with shfm, whichever way Run ends (os.Exit below
	// skips deferred calls).
	mounts := fusemount.NewManager(fusemount.DefaultBase())
	m.SetMountManager(mounts)

	// Alternate screen and mouse reporting are requested by the model's
	// View (bubbletea v2 has no program options for them).
	p := tea.NewProgram(m)
	if session != nil {
		// The application withdrew its request: nothing left to choose.
		session.WatchWithdrawn(p.Quit)
	}
	vfs.TerminalHandoff = ui.TerminalHandoff(p)
	// pkexec asks shfm itself for the password, in a dialog; without the
	// agent (no polkit, no system bus) it falls back to the terminal.
	if agent, err := polkitagent.Start(ui.AuthPrompter(p)); err != nil {
		applog.Warn("polkit agent not available", "error", err)
	} else {
		defer agent.Close()
	}
	_, err = p.Run()
	if session != nil {
		if reply, ok := m.PickReply(); ok {
			if err := session.Send(reply); err != nil {
				applog.Warn("could not answer the file chooser portal", "error", err)
			}
		}
		session.Close()
	}
	mounts.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
