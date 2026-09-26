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
	"context"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"shfm/internal/polkitagent"
)

// Password prompts of shfm's polkit agent (see package polkitagent): the
// agent, on its own goroutine, sends authPromptMsg and waits on reply; the
// dialog answers once, on Enter or Esc, or goes away (authWithdrawnMsg)
// when polkit cancels the request.

type authReply struct {
	text string
	ok   bool
}

type authPromptMsg struct {
	prompt polkitagent.Prompt
	reply  chan authReply
}

type authWithdrawnMsg struct{ reply chan authReply }

// authState is a pending prompt. polkit keeps waiting until it's
// answered, so the dialog stays on top of anything opened meanwhile
// (prev, shown again afterwards) — see keepAuthOnTop.
type authState struct {
	reply chan authReply
	dlg   Dialog
	prev  Dialog
}

// AuthPrompter returns the polkitagent.Prompter that asks through p's TUI.
func AuthPrompter(p *tea.Program) polkitagent.Prompter {
	return func(ctx context.Context, pr polkitagent.Prompt) (string, bool) {
		reply := make(chan authReply, 1)
		p.Send(authPromptMsg{prompt: pr, reply: reply})
		select {
		case r := <-reply:
			return r.text, r.ok
		case <-ctx.Done():
			p.Send(authWithdrawnMsg{reply: reply})
			return "", false
		}
	}
}

func (m *Model) handleAuthPrompt(msg authPromptMsg) {
	if m.auth != nil {
		// The agent asks one question at a time; a stale prompt can only
		// be one whose request is over.
		m.auth.reply <- authReply{}
		m.dialog = m.auth.prev
		m.auth = nil
	}
	ti := textinput.New()
	ti.CharLimit = 512
	ti.SetWidth(40)
	if !msg.prompt.Echo {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}
	ti.Focus()
	d := Dialog{Kind: DialogAuth, Title: "Authentication required", Inputs: []textinput.Model{ti}, Auth: msg.prompt}
	m.auth = &authState{reply: msg.reply, dlg: d, prev: m.dialog}
	m.dialog = d
}

func (m *Model) handleAuthWithdrawn(msg authWithdrawnMsg) {
	if m.auth != nil && m.auth.reply == msg.reply {
		m.dialog = m.auth.prev
		m.auth = nil
	}
}

func (m *Model) updateAuthDialogKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		m.answerAuth(authReply{text: m.dialog.Inputs[0].Value(), ok: true})
		return nil
	case "esc":
		m.answerAuth(authReply{})
		return nil
	}
	var cmd tea.Cmd
	m.dialog.Inputs[0], cmd = m.dialog.Inputs[0].Update(msg)
	return cmd
}

func (m *Model) answerAuth(r authReply) {
	if m.auth == nil {
		return
	}
	m.auth.reply <- r
	m.dialog = m.auth.prev
	m.auth = nil
}

// keepAuthOnTop runs after every update: while a prompt is pending, the
// dialog shown is the prompt, whatever else tried to open in between.
func (m *Model) keepAuthOnTop() {
	if m.auth == nil {
		return
	}
	if m.dialog.Kind == DialogAuth {
		m.auth.dlg = m.dialog
		return
	}
	m.auth.prev = m.dialog
	m.dialog = m.auth.dlg
}
