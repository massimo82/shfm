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

	tea "charm.land/bubbletea/v2"

	"shfm/internal/localsend"
)

// A message from another device is shown over the dialog open, which is
// back once it's closed; requests arriving meanwhile wait their turn.
func TestLocalSendMessageOverlay(t *testing.T) {
	m := archiveTestModel(t, t.TempDir())
	m.openNewItemChoice()

	first := &localsend.Request{From: localsend.Device{Alias: "phone"}, IsMessage: true, Message: "hello"}
	second := &localsend.Request{From: localsend.Device{Alias: "tablet"}, IsMessage: true, Message: "again"}
	m.Update(lsIncomingMsg{req: first})
	m.Update(lsIncomingMsg{req: second})
	if m.dialog.Kind != DialogLocalSendMessage || m.lsOverlay.req != first {
		t.Fatalf("dialog %v, overlay %+v", m.dialog.Kind, m.lsOverlay)
	}
	// Another dialog opened meanwhile goes under it.
	m.openHelp()
	m.Update(lsChangedMsg{})
	if m.dialog.Kind != DialogLocalSendMessage {
		t.Fatalf("overlay hidden by %v", m.dialog.Kind)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.dialog.Kind != DialogLocalSendMessage || m.lsOverlay.req != second {
		t.Fatalf("second message not shown: %v", m.dialog.Kind)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.lsOverlay != nil || m.dialog.Kind != DialogHelp {
		t.Fatalf("after the messages: %v, overlay %v", m.dialog.Kind, m.lsOverlay)
	}
}
