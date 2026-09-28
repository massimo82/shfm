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

// Package termlaunch runs a command in a new terminal emulator window: how
// shfm's desktop services (internal/filemanager1, internal/portal), which
// have no terminal of their own, show shfm to the user.
package termlaunch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// execArgs is how each known terminal is told to run a command: the
// arguments that go between the terminal and the command's own argv.
var execArgs = map[string][]string{
	"xdg-terminal-exec": nil,
	"foot":              nil,
	"footclient":        nil,
	"kitty":             nil,
	"alacritty":         {"-e"},
	"ghostty":           {"-e"},
	"wezterm":           {"start", "--"},
	"konsole":           {"-e"},
	"gnome-terminal":    {"--"},
	"kgx":               {"--"},
	"xfce4-terminal":    {"-x"},
	"mate-terminal":     {"-x"},
	"terminator":        {"-x"},
	"xterm":             {"-e"},
	"urxvt":             {"-e"},
	"st":                {"-e"},
}

// probeOrder is the order terminals are looked for when neither the
// configuration nor $TERMINAL names one: xdg-terminal-exec first, since it
// follows the user's own choice (the xdg-terminal-exec specification).
var probeOrder = []string{
	"xdg-terminal-exec", "foot", "alacritty", "kitty", "ghostty", "wezterm",
	"konsole", "gnome-terminal", "kgx", "xfce4-terminal", "mate-terminal",
	"terminator", "xterm", "urxvt", "st",
}

// lookPath and getenv are variables so tests can fake the installed
// terminals and the environment.
var (
	lookPath = exec.LookPath
	getenv   = os.Getenv
)

// ErrNoTerminal means no terminal emulator could be found.
var ErrNoTerminal = errors.New("no terminal emulator found: set \"terminal\" in shfm's config.json")

// Command returns the argv that runs args in a new terminal window.
// configured is the user's choice (config.json's "terminal"), taken as a
// command line prefix: a bare known terminal name ("alacritty") gets the
// arguments that terminal needs to run a command, anything else ("wezterm
// start --", "foot --app-id=shfm") is used as given, with args appended.
// Without it, $TERMINAL is used the same way, then the first installed of
// probeOrder.
func Command(configured string, args []string) ([]string, error) {
	for _, pref := range []string{configured, getenv("TERMINAL")} {
		fields := strings.Fields(pref)
		if len(fields) == 0 {
			continue
		}
		if _, err := lookPath(fields[0]); err != nil {
			continue
		}
		if len(fields) == 1 {
			flags, known := execArgs[filepath.Base(fields[0])]
			if !known {
				flags = []string{"-e"} // the most common convention
			}
			fields = append(fields, flags...)
		}
		return append(fields, args...), nil
	}
	for _, name := range probeOrder {
		if p, err := lookPath(name); err == nil {
			return append(append([]string{p}, execArgs[name]...), args...), nil
		}
	}
	return nil, ErrNoTerminal
}

// Start runs args in a new terminal window, detached from the caller (its
// own session, output discarded), and returns the started terminal
// process: it may exit right away, when the terminal hands the window
// over to an instance already running, so callers can't take its exit as
// the command's.
func Start(configured string, args []string) (*exec.Cmd, error) {
	argv, err := Command(configured, args)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start %s: %w", argv[0], err)
	}
	return cmd, nil
}
