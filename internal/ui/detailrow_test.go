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
	"time"

	"shfm/internal/vfs"
)

func TestDetailLineForFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}

	p := NewPane(vfs.NewLocalFS("Local", dir), dir, false, 0, nil)
	idx := -1
	for i, e := range p.Entries {
		if e.Name == "note.txt" {
			idx = i
		}
	}
	if idx == -1 {
		t.Fatalf("note.txt not found in listing: %+v", p.Entries)
	}
	p.Cursor = idx

	text := detailLineText(p)
	if !strings.Contains(text, "rw-r-----") {
		t.Errorf("expected the rwx permission string 'rw-r-----' in %q", text)
	}
	if !strings.Contains(text, "M:") {
		t.Errorf("expected a modification date (M:) in %q", text)
	}
	if !strings.Contains(text, "C:") {
		t.Errorf("expected a creation date field (C:) in %q", text)
	}
	// This sandbox's filesystem does support STATX_BTIME (confirmed by
	// internal/vfs's own tests), so the creation date should be populated,
	// not just the placeholder "-".
	if strings.Contains(text, "C:-") {
		t.Errorf("expected an actual creation date, got placeholder in %q", text)
	}
}

func TestDetailLineChangesWithCursor(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bb"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := NewPane(vfs.NewLocalFS("Local", dir), dir, false, 0, nil)
	idxA, idxB := -1, -1
	for i, e := range p.Entries {
		switch e.Name {
		case "a.txt":
			idxA = i
		case "b.txt":
			idxB = i
		}
	}
	if idxA == -1 || idxB == -1 {
		t.Fatalf("expected both a.txt and b.txt in the listing, got %+v", p.Entries)
	}

	p.Cursor = idxA
	aText := detailLineText(p)
	p.Cursor = idxB
	bText := detailLineText(p)

	if aText == bText {
		t.Errorf("detail line should change when the cursor moves to a different entry with different permissions, got the same text: %q", aText)
	}
	// a.txt is 0644 -> rw-r--r--, b.txt is 0600 -> rw-------
	if !strings.Contains(aText, "rw-r--r--") {
		t.Errorf("a.txt detail = %q, expected to contain rw-r--r--", aText)
	}
	if !strings.Contains(bText, "rw-------") {
		t.Errorf("b.txt detail = %q, expected to contain rw-------", bText)
	}
}

func TestDetailLineBlankOnParentEntry(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewPane(vfs.NewLocalFS("Local", dir), sub, false, 0, nil)
	if len(p.Entries) == 0 || !IsParentEntry(p.Entries[0]) {
		t.Fatalf("expected the '..' entry first, got %+v", p.Entries)
	}
	p.Cursor = 0
	if text := detailLineText(p); text != "" {
		t.Errorf("expected a blank detail line for the '..' entry, got %q", text)
	}
}

func TestDetailLineBlankOnEmptyFolder(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "empty")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewPane(vfs.NewLocalFS("Local", dir), dir, false, 0, nil)
	// An "empty" folder still has the ".." entry (dir has a parent), which
	// should also render a blank detail line, same as an entirely empty
	// listing would.
	if len(p.Entries) != 1 || !IsParentEntry(p.Entries[0]) {
		t.Fatalf("expected only the '..' entry, got %+v", p.Entries)
	}
	p.Cursor = 0
	if text := detailLineText(p); text != "" {
		t.Errorf("expected a blank detail line for an empty folder, got %q", text)
	}
}

func TestDetailLineBlankInTrashMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewPane(vfs.NewLocalFS("Local", dir), dir, false, 0, nil)
	p.Mode = PaneTrash
	p.Load()
	if text := detailLineText(p); text != "" {
		t.Errorf("expected a blank detail line while in trash mode, got %q", text)
	}
}

func TestDetailLineForSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("t"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	p := NewPane(vfs.NewLocalFS("Local", dir), dir, false, 0, nil)
	for i, e := range p.Entries {
		if e.Name == "link.txt" {
			p.Cursor = i
		}
	}
	e, ok := p.CurrentEntry()
	if !ok || !e.IsSymlink {
		t.Fatalf("expected the cursor to be on a symlink entry, got %+v (ok=%v)", e, ok)
	}
	text := detailLineText(p)
	if text == "" {
		t.Error("expected a non-blank detail line for a symlink")
	}
	if !strings.Contains(text, "M:") {
		t.Errorf("expected a modification date for the symlink itself in %q", text)
	}
}

func TestDetailLineOwnerGroupFallback(t *testing.T) {
	// A synthetic entry with no Owner/Group (as a remote backend without
	// that info might report) should fall back to "-" rather than showing
	// an empty, hard-to-read gap.
	e := vfs.Entry{Name: "remote-file", Mode: 0o644, ModTime: time.Now()}
	p := &Pane{Entries: []vfs.Entry{e}, Cursor: 0, FS: vfs.NewLocalFS("Local", ".")}
	text := detailLineText(p)
	if !strings.Contains(text, "-:-") {
		t.Errorf("expected owner:group to fall back to '-:-' when both are empty, got %q", text)
	}
}

// attrFS is a local filesystem that reports chattr flags on every entry, to
// exercise the detail line's flag display (real flags need root to set).
type attrFS struct {
	*vfs.LocalFS
	attrs []string
}

func (a attrFS) Attributes(string) []string { return a.attrs }

func TestDetailLineShowsAttributeFlags(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "locked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	build := func(attrs []string) string {
		p := NewPane(attrFS{vfs.NewLocalFS("Local", dir), attrs}, dir, false, 0, nil)
		for i, e := range p.Entries {
			if e.Name == "locked.txt" {
				p.Cursor = i
			}
		}
		return detailLineText(p)
	}

	if text := build(nil); strings.Contains(text, "[") {
		t.Errorf("no flags: nothing should be added, got %q", text)
	}
	text := build([]string{"immutable"})
	if !strings.Contains(text, "rw-r--r-- [immutable]") {
		t.Errorf("the flag should follow the permissions, got %q", text)
	}
	if text := build([]string{"immutable", "append-only"}); !strings.Contains(text, "[immutable,append-only]") {
		t.Errorf("both flags should be listed, got %q", text)
	}
}
