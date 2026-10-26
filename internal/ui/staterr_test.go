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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shfm/internal/vfs"
)

// TestUnreadableMetadataShowsQuestionMarks: in a folder that can be listed
// but not searched (r without x), entries' names are known but not their
// size, mode or owner. They must show as "?", like ls -l, never as a fake
// empty file with mode 0.
func TestUnreadableMetadataShowsQuestionMarks(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the missing x permission")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("not empty"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	m := newTestModel()
	p := NewPane(vfs.NewLocalFS("Local", dir), dir, false, m.active, m.sizeCh)
	m.panes[m.active] = p
	idx := map[string]int{}
	for i, e := range p.Entries {
		idx[e.Name] = i
	}
	fileIdx, ok := idx["config.yaml"]
	if !ok {
		t.Fatalf("config.yaml not listed: %+v", p.Entries)
	}
	if _, ok := idx["sub"]; !ok {
		t.Fatalf("sub not listed: %+v", p.Entries)
	}
	if e := p.Entries[idx["sub"]]; !e.IsDir || e.Size == SizePending {
		t.Errorf("sub: want a folder (from d_type) not queued for sizing, got %+v", e)
	}

	for _, l := range m.renderEntryLines(0, p, 60, 10) {
		if (strings.Contains(l, "config.yaml") || strings.Contains(l, "sub/")) && !strings.Contains(l, " ?") {
			t.Errorf("row should end in an unknown size \"?\": %q", l)
		}
	}

	p.Cursor = fileIdx
	text := detailLineText(p)
	if !strings.Contains(text, "?????????") || !strings.Contains(text, "permission denied") {
		t.Errorf("detail line should show unknown permissions and the reason, got %q", text)
	}

	m.openProperties()
	if len(m.dialog.Inputs) != 0 {
		t.Errorf("properties of an unreadable entry must be read-only, got %d inputs", len(m.dialog.Inputs))
	}
	if box := m.renderDialogBox(); !strings.Contains(box, "Details unreadable: permission denied") || !strings.Contains(box, "lacks the x permission") {
		t.Errorf("properties should explain why details are unknown:\n%s", box)
	}
}
