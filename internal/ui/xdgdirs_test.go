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

// mkdirs creates each named subdirectory of dir (helper for the tests below).
func mkdirs(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestLoadListsXDGDirsFirstInHome verifies that, when a pane's path is the
// user's home folder, Load() lists the standard XDG user directories first
// (alphabetically), then the rest of the folders and files, folders before
// files, alphabetically.
func TestLoadListsXDGDirsFirstInHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// XDG user dirs, deliberately created out of alphabetical order.
	mkdirs(t, home, "Videos", "Desktop", "Documents", "Downloads")
	// Non-XDG folders and a file, also out of order, including one whose
	// name would sort before some XDG dirs alphabetically.
	mkdirs(t, home, "Zeta", "Alpha")
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := NewPane(vfs.NewLocalFS("Local", home), home, false, 0, nil)

	var names []string
	for _, e := range p.Entries {
		if IsParentEntry(e) {
			continue
		}
		names = append(names, e.Name)
	}
	want := []string{"Desktop", "Documents", "Downloads", "Videos", "Alpha", "Zeta", "notes.txt"}
	if len(names) != len(want) {
		t.Fatalf("got entries %v, want %v", names, want)
	}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("entry %d = %q, want %q (full listing: %v)", i, names[i], n, names)
		}
	}

	for _, n := range []string{"Desktop", "Documents", "Downloads", "Videos"} {
		if !p.XDGNames[n] {
			t.Errorf("expected XDGNames[%q] to be true", n)
		}
	}
	for _, n := range []string{"Alpha", "Zeta", "notes.txt"} {
		if p.XDGNames[n] {
			t.Errorf("expected XDGNames[%q] to be false", n)
		}
	}
}

// TestLoadDoesNotFlagXDGDirsOutsideHome checks that a folder named e.g.
// "Documents" that isn't actually inside $HOME is treated as an ordinary
// folder, not specially highlighted or reordered.
func TestLoadDoesNotFlagXDGDirsOutsideHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	other := t.TempDir()
	mkdirs(t, other, "Documents", "Alpha")

	p := NewPane(vfs.NewLocalFS("Local", other), other, false, 0, nil)

	if len(p.XDGNames) != 0 {
		t.Errorf("expected no XDG names outside home, got %v", p.XDGNames)
	}
	var names []string
	for _, e := range p.Entries {
		if IsParentEntry(e) {
			continue
		}
		names = append(names, e.Name)
	}
	want := []string{"Alpha", "Documents"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Errorf("got %v, want plain alphabetical %v", names, want)
	}
}

// TestXDGUserDirsHonorsUserDirsDotDirs checks that a custom/localized
// user-dirs.dirs entry (as xdg-user-dirs-update would write, e.g. after a
// non-English locale renamed "Documents" to "Documenti") is picked up in
// place of the built-in default name.
func TestXDGUserDirsHonorsUserDirsDotDirs(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "XDG_DOCUMENTS_DIR=\"$HOME/Documenti\"\nXDG_DESKTOP_DIR=\"$HOME/Desktop\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "user-dirs.dirs"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", configDir)

	dirs := xdgUserDirs(home)
	if !dirs[filepath.Join(home, "Documenti")] {
		t.Errorf("expected Documenti (from user-dirs.dirs) to be recognized: %v", dirs)
	}
	if dirs[filepath.Join(home, "Documents")] {
		t.Errorf("expected the overridden default Documents to NOT be present: %v", dirs)
	}
	if !dirs[filepath.Join(home, "Desktop")] {
		t.Errorf("expected Desktop (explicitly listed) to be recognized: %v", dirs)
	}
	// Other vars not present in the file fall back to their defaults.
	if !dirs[filepath.Join(home, "Downloads")] {
		t.Errorf("expected Downloads (fallback default) to be recognized: %v", dirs)
	}
}
