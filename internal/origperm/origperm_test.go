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
package origperm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRememberKeepsFirstOriginalAcrossReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "orig.json")
	s := Load(path)
	first := Original{Mode: os.ModeDir | os.ModeSetgid | 0o766, Owner: "root", Group: "root"}
	if err := s.Remember("local|Local|/x", first); err != nil {
		t.Fatal(err)
	}
	// A second change of the same path must not overwrite the original.
	if err := s.Remember("local|Local|/x", Original{Mode: 0o777}); err != nil {
		t.Fatal(err)
	}

	got, ok := Load(path).Get("local|Local|/x")
	if !ok || got.Mode != first.Mode || got.Owner != "root" || got.Group != "root" || got.Saved.IsZero() {
		t.Fatalf("reloaded original = %+v, %v; want %+v", got, ok, first)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("state file should be private: %v %v", info, err)
	}

	s = Load(path)
	if err := s.Forget("local|Local|/x"); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(path).Get("local|Local|/x"); ok {
		t.Error("original still there after Forget")
	}
}

func TestLoadToleratesMissingAndCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	if _, ok := Load(filepath.Join(dir, "none.json")).Get("k"); ok {
		t.Error("missing file should be an empty store")
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{not json"), 0o600)
	s := Load(bad)
	if err := s.Remember("k", Original{Mode: 0o644}); err != nil {
		t.Fatal(err)
	}
	if _, ok := Load(bad).Get("k"); !ok {
		t.Error("a corrupt file should be replaced on the next save")
	}
}

func TestDefaultPathFollowsXDGStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/synthetic/state")
	if got := DefaultPath(); got != "/synthetic/state/shfm/original-permissions.json" {
		t.Errorf("DefaultPath = %q", got)
	}
}
