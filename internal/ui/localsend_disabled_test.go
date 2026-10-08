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

//go:build !localsend

package ui

import (
	"strings"
	"testing"
)

// Without the LocalSend module: its shortcut says so, About doesn't
// mention it.
func TestLocalSendUnavailable(t *testing.T) {
	m := archiveTestModel(t, t.TempDir())
	m.openLocalSend()
	if m.dialog.Kind != DialogNone || !strings.Contains(m.status, "localsend") {
		t.Fatalf("dialog %v, status %q", m.dialog.Kind, m.status)
	}
	if strings.Contains(aboutDescription(), "LocalSend") {
		t.Fatalf("About mentions LocalSend: %s", aboutDescription())
	}
}
