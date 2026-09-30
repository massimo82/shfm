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

package nerdfont

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fake points the package at the given font folders and fc-list output
// (nil output: no fc-list), restoring the real ones when the test ends.
func fake(t *testing.T, dirs []string, fcList []byte) {
	t.Helper()
	oldDirs, oldFind, oldRun := fontDirs, findTool, runTool
	t.Cleanup(func() { fontDirs, findTool, runTool = oldDirs, oldFind, oldRun })
	fontDirs = func() []string { return dirs }
	findTool = func(name string) (string, error) {
		if fcList == nil {
			return "", errors.New("not found")
		}
		return "/fake/" + name, nil
	}
	runTool = func(string, ...string) ([]byte, error) { return fcList, nil }
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFindInFontDirs: a Nerd Font file in a subfolder of any font folder
// is found, in either naming scheme; other fonts and non-font files
// named like one aren't.
func TestFindInFontDirs(t *testing.T) {
	for _, tc := range []struct {
		file, want string
	}{
		{"TTF/SymbolsNerdFont-Regular.ttf", "SymbolsNerdFont-Regular"},
		{"hack/Hack Regular Nerd Font Complete.otf", "Hack Regular Nerd Font Complete"},
		{"OTF/FiraCodeNerdFontMono-Bold.OTF", "FiraCodeNerdFontMono-Bold"},
		{"TTF/DejaVuSans.ttf", ""},
		{"docs/NerdFont-README.txt", ""},
	} {
		empty, sys := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(sys, tc.file))
		fake(t, []string{filepath.Join(empty, "missing"), empty, sys}, nil)
		if got := Find(); got != tc.want {
			t.Errorf("with %s: Find() = %q, want %q", tc.file, got, tc.want)
		}
	}
}

// TestFindWithFontconfig: with no Nerd Font in the font folders, the
// families fc-list reports, a font's aliases included, are checked.
func TestFindWithFontconfig(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "DejaVuSans.ttf"))

	fake(t, []string{dir}, []byte("DejaVu Sans\nNoto Sans,Noto Sans Display\nJetBrainsMono NFM,JetBrainsMono Nerd Font Mono\n"))
	if got := Find(); got != "JetBrainsMono Nerd Font Mono" {
		t.Errorf("Find() = %q, want the Nerd Font family fc-list reports", got)
	}

	fake(t, []string{dir}, []byte("DejaVu Sans\nNoto Sans\n"))
	if got := Find(); got != "" {
		t.Errorf("Find() = %q with no Nerd Font family, want \"\"", got)
	}

	fake(t, []string{dir}, nil)
	if got := Find(); got != "" {
		t.Errorf("Find() = %q with no fc-list, want \"\"", got)
	}
}

// TestDefaultFontDirs: the XDG data folders' fonts, ~/.fonts included,
// with the spec's defaults when the variables are unset.
func TestDefaultFontDirs(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_DATA_DIRS", "")
	want := []string{"/h/.local/share/fonts", "/h/.fonts", "/usr/local/share/fonts", "/usr/share/fonts"}
	if got := defaultFontDirs(); !equal(got, want) {
		t.Errorf("defaultFontDirs() = %v, want %v", got, want)
	}

	t.Setenv("XDG_DATA_HOME", "/data")
	t.Setenv("XDG_DATA_DIRS", "/a:/b")
	want = []string{"/data/fonts", "/h/.fonts", "/a/fonts", "/b/fonts"}
	if got := defaultFontDirs(); !equal(got, want) {
		t.Errorf("defaultFontDirs() = %v, want %v", got, want)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
