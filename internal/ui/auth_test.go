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
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/polkitagent"
)

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestAuthPromptStaysOnTopAndAnswers(t *testing.T) {
	m, _ := mirrorTestModel(t)
	m.dialog = Dialog{Kind: DialogHelp}
	reply := make(chan authReply, 1)
	m.Update(authPromptMsg{prompt: polkitagent.Prompt{Message: "Run chown", User: "alice", Text: "Password: "}, reply: reply})
	if m.dialog.Kind != DialogAuth {
		t.Fatalf("dialog = %v, want the password prompt", m.dialog.Kind)
	}

	// Something else opens a dialog meanwhile: the prompt stays in front.
	m.dialog = Dialog{Kind: DialogMessage, Message: "later"}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.dialog.Kind != DialogAuth {
		t.Fatalf("dialog = %v, the pending prompt must stay on top", m.dialog.Kind)
	}

	typeText(m, "s3cret")
	if box := m.renderDialogBox(); strings.Contains(box, "s3cret") || !strings.Contains(box, "alice") {
		t.Errorf("prompt must mask the password and name the user:\n%s", box)
	}
	m.Update(teaEnterMsg())
	if r := <-reply; !r.ok || r.text != "s3cret" {
		t.Errorf("reply = %+v, want the typed password", r)
	}
	if m.dialog.Kind != DialogMessage || m.auth != nil {
		t.Errorf("after answering, the dialog opened meanwhile should be back, got %v", m.dialog.Kind)
	}
}

func TestAuthPromptEscAndWithdraw(t *testing.T) {
	m, _ := mirrorTestModel(t)
	reply := make(chan authReply, 1)
	m.Update(authPromptMsg{prompt: polkitagent.Prompt{Text: "Password:"}, reply: reply})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if r := <-reply; r.ok {
		t.Error("Esc must dismiss the prompt")
	}
	if m.dialog.Kind != DialogNone {
		t.Errorf("dialog = %v after Esc", m.dialog.Kind)
	}

	reply2 := make(chan authReply, 1)
	m.Update(authPromptMsg{prompt: polkitagent.Prompt{Text: "Password:"}, reply: reply2})
	m.Update(authWithdrawnMsg{reply: reply2})
	if m.dialog.Kind != DialogNone || m.auth != nil {
		t.Errorf("a withdrawn request must close its prompt, dialog = %v", m.dialog.Kind)
	}
}
