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
)

func TestHelpCarousel(t *testing.T) {
	m := newTestModel()

	// Wide enough: the line stays still and no tick runs.
	m.width, m.height = len(paneHelpHints)+10, 30
	if m.ensureHelpTick() != nil || m.helpTicking {
		t.Fatal("carousel started for a line that fits")
	}
	if got := m.helpLineText(paneHelpHints, m.width-2); got != paneHelpHints {
		t.Fatalf("fitting line = %q", got)
	}

	// Too narrow: it starts at the beginning, then scrolls by one cell a tick.
	m.width = 40
	if m.ensureHelpTick() == nil || !m.helpTicking {
		t.Fatal("carousel did not start for an overflowing line")
	}
	if m.ensureHelpTick() != nil {
		t.Fatal("a second tick was started")
	}
	w, r := m.width-2, []rune(paneHelpHints)
	if got := m.helpLineText(paneHelpHints, w); got != string(r[:w]) {
		t.Fatalf("first frame = %q", got)
	}
	if m.handleHelpTick() == nil {
		t.Fatal("carousel stopped while overflowing")
	}
	if got := m.helpLineText(paneHelpHints, w); got != string(r[1:w+1]) {
		t.Fatalf("second frame = %q", got)
	}

	// At the end the text comes back round after the gap.
	m.helpOffset = len(r) - 3
	got := m.helpLineText(paneHelpHints, w)
	want := string(r[len(r)-3:]) + helpCarouselGap
	if !strings.HasPrefix(got, want) || !strings.HasSuffix(got, string(r[:w-len([]rune(want))])) {
		t.Fatalf("wrapping frame = %q", got)
	}
	if n := len([]rune(got)); n != w {
		t.Fatalf("frame is %d cells, want %d", n, w)
	}

	// A dialog hides the line: the tick stops and the next lap starts over.
	m.dialog.Kind = DialogHelp
	if m.handleHelpTick() != nil || m.helpTicking || m.helpOffset != 0 {
		t.Fatal("carousel kept running under a dialog")
	}
}
