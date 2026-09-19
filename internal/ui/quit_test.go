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
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"shfm/internal/fileops"
)

// isQuitCmd reports whether cmd, when invoked, produces bubbletea's
// tea.QuitMsg — the only thing that actually makes the program exit.
// Setting Model.quitting alone does nothing: View() would just render an
// empty screen forever while the event loop keeps running, which is
// exactly the "pressing q freezes everything" bug this file guards
// against.
func isQuitCmd(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestPressingQActuallyQuits is the direct regression test for the report
// "pressing q freezes the whole interface": with no background tasks
// running, pressing q must yield a command that actually terminates the
// bubbletea program, not just flip an internal flag no one acts on.
func TestPressingQActuallyQuits(t *testing.T) {
	m := newTestModel()
	if m.hasRunningTasks() {
		t.Fatal("test setup: a freshly created model shouldn't have running tasks")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !isQuitCmd(cmd) {
		t.Fatal("pressing q with no background tasks must return tea.Quit; got a command that does not produce tea.QuitMsg (or a nil command) — this is exactly the freeze bug")
	}
	if !m.quitting {
		t.Error("Model.quitting should also be set to true")
	}
}

// TestCtrlQActuallyQuits mirrors the above for the Ctrl+Q alias.
func TestCtrlQActuallyQuits(t *testing.T) {
	m := newTestModel()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if !isQuitCmd(cmd) {
		t.Fatal("Ctrl+Q with no background tasks must return tea.Quit")
	}
}

// TestQWithRunningTaskAsksFirst verifies q does NOT quit immediately when
// a background task is still running — it should open the warning dialog
// instead, exactly as designed.
func TestQWithRunningTaskAsksFirst(t *testing.T) {
	m := newTestModel()
	m.startTask(TaskCopy, 1, func(prog *fileops.Progress) *fileops.Result {
		<-make(chan struct{}) // never finishes on its own
		return &fileops.Result{}
	})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if isQuitCmd(cmd) {
		t.Fatal("q should NOT quit immediately while a background task is running")
	}
	if m.quitting {
		t.Fatal("Model.quitting must stay false until the user confirms")
	}
	if m.dialog.Kind != DialogConfirmQuit {
		t.Fatalf("expected DialogConfirmQuit to open, got %v", m.dialog.Kind)
	}
}

// TestConfirmingQuitDialogActuallyQuits is the regression test for the
// second, identical instance of the same bug: confirming "quit anyway?"
// after being warned about running background tasks must also return a
// real tea.Quit, not just flip the flag.
func TestConfirmingQuitDialogActuallyQuits(t *testing.T) {
	m := newTestModel()
	m.startTask(TaskCopy, 1, func(prog *fileops.Progress) *fileops.Result {
		<-make(chan struct{})
		return &fileops.Result{}
	})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if m.dialog.Kind != DialogConfirmQuit {
		t.Fatalf("test setup: expected DialogConfirmQuit, got %v", m.dialog.Kind)
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !isQuitCmd(cmd) {
		t.Fatal("confirming the quit-anyway dialog must return tea.Quit — this is the exact same freeze bug on a second path")
	}
	if !m.quitting {
		t.Error("Model.quitting should also be set to true")
	}
}

// TestDecliningQuitDialogDoesNotQuit verifies the "n" / Esc path of the
// same dialog correctly does NOT quit, for completeness/symmetry.
func TestDecliningQuitDialogDoesNotQuit(t *testing.T) {
	m := newTestModel()
	m.startTask(TaskCopy, 1, func(prog *fileops.Progress) *fileops.Result {
		<-make(chan struct{})
		return &fileops.Result{}
	})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if isQuitCmd(cmd) {
		t.Fatal("declining the quit-anyway dialog must not quit")
	}
	if m.quitting {
		t.Fatal("Model.quitting must stay false after declining")
	}
	if m.dialog.Kind != DialogNone {
		t.Errorf("expected the dialog to close after declining, got %v", m.dialog.Kind)
	}
}
