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
	"time"
)

func TestFileChangedDetectsMtimeAndSizeChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if fileChanged(before, path) {
		t.Error("untouched file reported as changed")
	}

	// Same size, but a later mtime: still a change (e.g. an editor that
	// rewrites identical content still counts as "the app touched it").
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if !fileChanged(before, path) {
		t.Error("later mtime should be reported as changed")
	}

	// Different size, mtime reset back to match "before": still a change.
	if err := os.WriteFile(path, []byte("hello, edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	if !fileChanged(before, path) {
		t.Error("different size should be reported as changed")
	}
}

func TestFileChangedMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gone.txt")
	if fileChanged(nil, path) {
		t.Error("a file that was never created should not be reported as changed")
	}
}

func TestFileChangedNilBeforeMeansUnconditionalChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileChanged(nil, path) {
		t.Error("a nil 'before' snapshot (download failed to stat) should be treated as changed, to be safe")
	}
}
