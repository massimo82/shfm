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
	"testing"

	"shfm/internal/vfs"
)

func TestSearchMatcherPlain(t *testing.T) {
	match, err := searchMatcher("Report.TXT", false)
	if err != nil {
		t.Fatal(err)
	}
	if !match("report.txt") {
		t.Error("plain mode should match case-insensitively")
	}
	if !match("report.txt.bak") {
		t.Error("plain mode should match the query as a substring of a longer name")
	}
	if match("report") {
		t.Error("plain mode must not match a name shorter than the query")
	}
}

// TestSearchMatcherPlainPartialQuery is the actual "type to narrow the
// list" case: a query shorter than any real filename should still match
// every name containing it, not just an exact full-name equal.
func TestSearchMatcherPlainPartialQuery(t *testing.T) {
	match, err := searchMatcher("doc", false)
	if err != nil {
		t.Fatal(err)
	}
	if !match("document.txt") {
		t.Error(`plain mode: "doc" should match "document.txt" as a substring`)
	}
	if !match("my_docs") {
		t.Error(`plain mode: "doc" should match "my_docs" as a substring`)
	}
	if match("readme.md") {
		t.Error(`plain mode: "doc" should not match "readme.md"`)
	}
}

func TestSearchMatcherRegex(t *testing.T) {
	match, err := searchMatcher(`^img_\d+\.jpe?g$`, true)
	if err != nil {
		t.Fatal(err)
	}
	if !match("IMG_0042.jpg") {
		t.Error("regex mode should match case-insensitively")
	}
	if !match("img_1.jpeg") {
		t.Error("regex mode should match img_1.jpeg")
	}
	if match("img_x.jpg") {
		t.Error("regex mode should not match a non-numeric suffix")
	}
}

func TestSearchMatcherInvalidRegex(t *testing.T) {
	if _, err := searchMatcher("(unclosed", true); err == nil {
		t.Error("an invalid regex should return an error")
	}
}

func TestSearchMatcherEmptyQueryMatchesEverything(t *testing.T) {
	match, err := searchMatcher("", false)
	if err != nil {
		t.Fatal(err)
	}
	if !match("anything.txt") {
		t.Error("an empty query should match everything")
	}
}

func TestWalkSearchFindsNestedMatches(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "notes.txt"), "top level")
	mustMkdir(t, filepath.Join(root, "sub"))
	mustWriteFile(t, filepath.Join(root, "sub", "notes.txt"), "nested")
	mustMkdir(t, filepath.Join(root, "sub", "deeper"))
	mustWriteFile(t, filepath.Join(root, "sub", "deeper", "notes.txt"), "deeply nested")
	mustWriteFile(t, filepath.Join(root, "sub", "other.txt"), "not a match")

	fs := vfs.NewLocalFS("Local", root)
	match, err := searchMatcher("notes.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	results, truncated := walkSearch(fs, fs.Root(), match, func() bool { return false })
	if truncated {
		t.Error("small tree should not be reported as truncated")
	}
	want := map[string]bool{"notes.txt": true, "sub/notes.txt": true, "sub/deeper/notes.txt": true}
	if len(results) != len(want) {
		t.Fatalf("got %d results, want %d: %#v", len(results), len(want), results)
	}
	for _, r := range results {
		if !want[r.Name] {
			t.Errorf("unexpected result %q", r.Name)
		}
	}
}

func TestWalkSearchDoesNotDescendIntoSymlinkedDirs(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "real"))
	mustWriteFile(t, filepath.Join(root, "real", "target.txt"), "x")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	fs := vfs.NewLocalFS("Local", root)
	match, err := searchMatcher("target.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	results, _ := walkSearch(fs, fs.Root(), match, func() bool { return false })
	if len(results) != 1 {
		t.Fatalf("expected to find target.txt once via the real path only, got %#v", results)
	}
	if results[0].Name != "real/target.txt" {
		t.Errorf("expected real/target.txt, got %q (symlink must not have been followed)", results[0].Name)
	}
}

func TestWalkSearchRespectsCancellation(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		d := filepath.Join(root, "d", string(rune('a'+i)))
		mustMkdir(t, d)
		mustWriteFile(t, filepath.Join(d, "match.txt"), "x")
	}
	fs := vfs.NewLocalFS("Local", root)
	match, err := searchMatcher("match.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	results, truncated := walkSearch(fs, fs.Root(), match, func() bool { return true })
	if len(results) != 0 {
		t.Errorf("a search cancelled before it starts should return no results, got %#v", results)
	}
	if truncated {
		t.Error("a cancelled search should not also report truncated")
	}
}

func TestPaneFilterNarrowsListing(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "report.txt"), "x")
	mustWriteFile(t, filepath.Join(dir, "report.bak"), "x")
	mustWriteFile(t, filepath.Join(dir, "photo.jpg"), "x")
	mustMkdir(t, filepath.Join(dir, "reports"))

	p := NewPane(vfs.NewLocalFS("Local", dir), dir, false, 0, nil)
	if len(p.Entries) != 5 { // 4 real entries + the ".." parent entry
		t.Fatalf("expected 5 entries before filtering, got %d", len(p.Entries))
	}

	// The ".." entry is always shown, filtered or not, so navigating out of
	// a filtered view is never blocked.
	p.SetFilter("report", true) // regex: substring match
	names := entryNames(p)
	want := map[string]bool{"..": true, "report.txt": true, "report.bak": true, "reports": true}
	if len(names) != len(want) {
		t.Fatalf("regex filter: got %v, want keys of %v", names, want)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("regex filter: unexpected entry %q", n)
		}
	}

	// Plain (non-regex) mode: a partial query ("report", not the full
	// "report.txt") is the real "type to narrow the list" case — it should
	// still match everything containing it, not just an exact full-name
	// equal (that was the actual bug this test now guards against).
	p.SetFilter("report", false)
	names = entryNames(p)
	want = map[string]bool{"..": true, "report.txt": true, "report.bak": true, "reports": true}
	if len(names) != len(want) {
		t.Fatalf("plain filter: got %v, want keys of %v", names, want)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("plain filter: unexpected entry %q", n)
		}
	}

	p.ClearFilter()
	if len(p.Entries) != 5 {
		t.Errorf("ClearFilter should restore the full listing, got %d entries", len(p.Entries))
	}
	if p.FilterActive {
		t.Error("FilterActive should be false after ClearFilter")
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}
