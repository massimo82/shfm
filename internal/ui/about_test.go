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

	"shfm/internal/version"
)

// TestAboutDialog: Ctrl+Alt+A opens About with the name and version, a
// description, the website, the author's line and the copyright notice;
// any key closes it.
func TestAboutDialog(t *testing.T) {
	m := newTestModel()
	m.Update(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl | tea.ModAlt})
	if m.dialog.Kind != DialogAbout {
		t.Fatalf("Ctrl+Alt+A opened dialog %v, want DialogAbout", m.dialog.Kind)
	}
	box := m.renderDialogBox()
	for _, want := range []string{
		"Shell File Manager v" + version.Version,
		"shfm is a file manager for the terminal.",
		"https://massimo82.github.io/shfm/",
		"Designed by Massimo Cavalleri in Milan, Italy :)",
		"Copyright (C) 2026 Massimo Cavalleri",
		"GNU General Public License",
		"The third-party code bundled",
	} {
		if !strings.Contains(box, want) {
			t.Errorf("About dialog lacks %q:\n%s", want, box)
		}
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.dialog.Kind != DialogNone {
		t.Errorf("a key left dialog %v open, want it closed", m.dialog.Kind)
	}
}
