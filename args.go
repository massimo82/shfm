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

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"shfm/internal/applog"
	"shfm/internal/config"
	"shfm/internal/termlaunch"
	"shfm/internal/ui"
)

const usage = `Usage:
  shfm [PATH|URI]...           open a folder, or show files selected in their folder
  shfm --select PATH...        show the paths selected in their folder, folders too
  shfm --properties PATH       show PATH in its folder, with its properties open
  shfm --filemanager1          provide org.freedesktop.FileManager1 on the session bus
  shfm --portal                provide xdg-desktop-portal's file chooser backend
  shfm --pick SOCKET           file chooser mode (run by --portal)
`

// serviceIdleExit is how long the D-Bus services stay up unused: the bus
// starts them again on demand.
const serviceIdleExit = 5 * time.Minute

type options struct {
	start        ui.Start
	fileManager1 bool
	portal       bool
	pickSocket   string
	help         bool
}

// parseArgs parses shfm's command line: options first, then paths (or
// file:// URIs), with "--" ending the options.
func parseArgs(args []string) (options, error) {
	var o options
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			o.start.Paths = append(o.start.Paths, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			o.start.Paths = append(o.start.Paths, a)
			continue
		}
		switch a {
		case "-h", "--help":
			o.help = true
		case "--select":
			o.start.Select = true
		case "--properties":
			o.start.Properties = true
		case "--filemanager1":
			o.fileManager1 = true
		case "--portal":
			o.portal = true
		case "--pick":
			if i+1 >= len(args) {
				return o, errors.New("--pick needs the socket path")
			}
			i++
			o.pickSocket = args[i]
		default:
			return o, fmt.Errorf("unknown option %s", a)
		}
	}
	return o, nil
}

// selfPath is shfm's own executable, to run it in a terminal.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// startShfmInTerminal runs shfm with args in a new terminal window.
func startShfmInTerminal(cfg *config.Config, args []string) error {
	exited, err := startShfmInTerminalWait(cfg, args)
	if err != nil {
		return err
	}
	go func() {
		if err := <-exited; err != nil {
			applog.Warn("terminal exited with an error", "error", err)
		}
	}()
	return nil
}

// startShfmInTerminalWait is startShfmInTerminal, delivering the
// terminal's exit.
func startShfmInTerminalWait(cfg *config.Config, args []string) (<-chan error, error) {
	exe, err := selfPath()
	if err != nil {
		return nil, err
	}
	cmd, err := termlaunch.Start(cfg.Terminal, append([]string{exe}, args...))
	if err != nil {
		return nil, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	return exited, nil
}

// serveOrExit reports a D-Bus service's failure; it's run by the bus, so
// the message goes to both stderr (the user's journal) and shfm's log.
func serveOrExit(err error) {
	if err == nil {
		return
	}
	applog.Error("service failed", "error", err)
	fmt.Fprintln(os.Stderr, "shfm:", err)
	os.Exit(1)
}
