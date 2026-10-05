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
	"time"

	tea "charm.land/bubbletea/v2"
)

// The help line scrolls as a carousel when its hints don't fit the
// terminal: the text runs leftwards and comes back round after
// helpCarouselGap, resting at its start for helpCarouselRest on every lap
// so the first shortcuts can be read. A line that fits stays still, and no
// tick runs for it.
const (
	helpCarouselStep = 130 * time.Millisecond
	helpCarouselRest = 2 * time.Second
	helpCarouselGap  = " # "
)

type helpTickMsg struct{}

// helpHints is the help line's text for the current mode.
func (m *Model) helpHints() string {
	if m.picker != nil {
		return m.pickHints()
	}
	return paneHelpHints
}

// helpLineText returns the w cells of hints the help line shows now.
func (m *Model) helpLineText(hints string, w int) string {
	if hints != m.helpText {
		// Another mode's hints start over from their beginning.
		m.helpText, m.helpOffset = hints, 0
	}
	r := []rune(hints)
	if w <= 0 || len(r) <= w {
		return truncate(hints, w)
	}
	loop := append(r, []rune(helpCarouselGap)...)
	off := m.helpOffset % len(loop)
	out := make([]rune, 0, w)
	for i := 0; i < w; i++ {
		out = append(out, loop[(off+i)%len(loop)])
	}
	return string(out)
}

// helpOverflows reports whether the help line is shown and too long for
// it, i.e. whether the carousel has to run.
func (m *Model) helpOverflows() bool {
	if m.width == 0 || m.quitting || m.dialog.Kind != DialogNone {
		return false
	}
	return len([]rune(m.helpHints())) > m.width-2
}

// ensureHelpTick starts the carousel's tick when the help line needs it
// and it isn't running.
func (m *Model) ensureHelpTick() tea.Cmd {
	if m.helpTicking || !m.helpOverflows() {
		return nil
	}
	m.helpTicking = true
	return helpTick(m.helpDelay())
}

func helpTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return helpTickMsg{} })
}

// helpDelay is the wait before the next step: longer at the lap's start.
func (m *Model) helpDelay() time.Duration {
	if n := len([]rune(m.helpHints())) + len([]rune(helpCarouselGap)); m.helpOffset%n == 0 {
		return helpCarouselRest
	}
	return helpCarouselStep
}

// handleHelpTick advances the carousel by one cell, or stops it when the
// line fits again or is hidden (a dialog covers the screen); it rests at the
// start of the text when it comes back.
func (m *Model) handleHelpTick() tea.Cmd {
	if !m.helpOverflows() {
		m.helpTicking = false
		m.helpOffset = 0
		return nil
	}
	m.helpOffset = (m.helpOffset + 1) % (len([]rune(m.helpHints())) + len([]rune(helpCarouselGap)))
	return helpTick(m.helpDelay())
}
