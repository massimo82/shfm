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

package vfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBirthTimeOnFreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.txt")
	before := time.Now().Add(-2 * time.Second)
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := time.Now().Add(2 * time.Second)

	bt := birthTime(path, false)
	if bt.IsZero() {
		t.Skip("this filesystem doesn't record a birth time (STATX_BTIME unset) — nothing to verify")
	}
	if bt.Before(before) || bt.After(after) {
		t.Errorf("birthTime = %v, expected to fall between %v and %v", bt, before, after)
	}
}

func TestBirthTimeNonexistentFile(t *testing.T) {
	bt := birthTime("/nonexistent/path/for/sure", false)
	if !bt.IsZero() {
		t.Errorf("expected zero time for a nonexistent path, got %v", bt)
	}
}

func TestLocalFSBirthTimeMatchesHelper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := NewLocalFS("Local", dir)

	// Confirms LocalFS implements vfs.BirthTimer, as internal/ui relies on
	// via a type assertion.
	var _ BirthTimer = fs

	got, err := fs.BirthTime(path)
	if err != nil {
		t.Fatal(err)
	}
	want := birthTime(path, false)
	if !got.Equal(want) {
		t.Errorf("LocalFS.BirthTime = %v, want %v (from the birthTime helper)", got, want)
	}
}

func TestStatPopulatesCreationTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := NewLocalFS("Local", dir)
	entry, err := fs.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	direct := birthTime(path, false)
	if !entry.CreationTime.Equal(direct) {
		t.Errorf("Stat().CreationTime = %v, want %v", entry.CreationTime, direct)
	}
}

func TestListDoesNotPopulateCreationTime(t *testing.T) {
	// List() deliberately skips the per-entry birth-time syscall (it would
	// cost one extra statx per file in the directory, undermining the
	// "opening a folder never blocks" goal for large directories): only
	// Stat() and BirthTime(), both single-entry lookups, populate it.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs := NewLocalFS("Local", dir)
	entries, err := fs.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == "c.txt" && !e.CreationTime.IsZero() {
			t.Errorf("List() unexpectedly populated CreationTime for %q; this should stay a List()-time no-op by design", e.Name)
		}
	}
}
