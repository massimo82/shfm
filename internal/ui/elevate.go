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
	"os/exec"

	tea "charm.land/bubbletea/v2"
)

// terminalHandoffMsg asks the event loop to run cmd with the terminal
// released to it (see vfs.TerminalHandoff); done receives cmd's error.
type terminalHandoffMsg struct {
	cmd  *exec.Cmd
	done chan error
}

// handoffDoneMsg is the (ignored) message tea.ExecProcess sends back once
// the command has exited and the TUI has taken the terminal back.
type handoffDoneMsg struct{}

// TerminalHandoff returns a vfs.TerminalHandoff for p: pkexec's text
// password prompt (used when no graphical PolicyKit agent is running) gets
// the terminal to itself instead of being written over the TUI, which
// repaints once it exits. It blocks until then, so it must be called off
// the event loop — elevated operations run as commands or tasks.
func TerminalHandoff(p *tea.Program) func(*exec.Cmd) error {
	return func(cmd *exec.Cmd) error {
		done := make(chan error, 1)
		p.Send(terminalHandoffMsg{cmd: cmd, done: done})
		return <-done
	}
}

func (m *Model) handleTerminalHandoff(msg terminalHandoffMsg) tea.Cmd {
	return tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
		msg.done <- err
		return handoffDoneMsg{}
	})
}

// elevatedDoneMsg reports the outcome of an operation run by
// runElevatable: a status line on success, an error line otherwise.
type elevatedDoneMsg struct {
	status string
	err    string
}

// runElevatable runs op — a filesystem change that may fall back to
// pkexec — as a command, off the event loop, so that a text password
// prompt can take over the terminal (see TerminalHandoff) instead of
// deadlocking the loop it needs.
func runElevatable(op func() elevatedDoneMsg) tea.Cmd {
	return func() tea.Msg { return op() }
}

func (m *Model) handleElevatedDone(msg elevatedDoneMsg) {
	m.activePane().Load()
	if msg.err != "" {
		m.setError("%s", msg.err)
	} else {
		m.setStatus("%s", msg.status)
	}
}
