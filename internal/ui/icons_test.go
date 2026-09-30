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

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"shfm/internal/config"
	"shfm/internal/vfs"
)

// TestNerdEntryIcon: links by what they point to, folders by kind, files
// by name, extension (any case) or executable bit, the generic icon last.
func TestNerdEntryIcon(t *testing.T) {
	for _, tc := range []struct {
		desc    string
		e       vfs.Entry
		special string
		want    string
	}{
		{"parent", vfs.Entry{Name: "..", IsDir: true}, "", nerdParent},
		{"folder", vfs.Entry{Name: "src", IsDir: true}, "", nerdFolder},
		{"XDG music", vfs.Entry{Name: "Musica", IsDir: true}, "XDG_MUSIC_DIR", nerdMusicDir},
		{"XDG desktop", vfs.Entry{Name: "Desktop", IsDir: true}, "XDG_DESKTOP_DIR", nerdDesktopDir},
		{"trash", vfs.Entry{Name: ".Trash-1000", IsDir: true}, specialTrash, nerdTrash},
		{"link to folder", vfs.Entry{Name: "l", IsDir: true, IsSymlink: true}, "", nerdLinkDir},
		{"link to XDG folder", vfs.Entry{Name: "Music", IsDir: true, IsSymlink: true}, "XDG_MUSIC_DIR", nerdLinkDir},
		{"link to file", vfs.Entry{Name: "a.go", IsSymlink: true}, "", nerdLinkFile},
		{"by name", vfs.Entry{Name: "Makefile"}, "", ""},
		{"by name, dotfile", vfs.Entry{Name: ".gitignore"}, "", nerdGit},
		{"by extension", vfs.Entry{Name: "main.go"}, "", ""},
		{"by extension, upper case", vfs.Entry{Name: "PHOTO.JPG"}, "", nerdImageFile},
		{"known extension over executable", vfs.Entry{Name: "run.sh", Mode: 0o755}, "", nerdShell},
		{"executable", vfs.Entry{Name: "shfm", Mode: 0o755}, "", nerdExecutable},
		{"unknown type", vfs.Entry{Name: "data.xyz", Mode: 0o644}, "", nerdFile},
		{"no extension", vfs.Entry{Name: "notes"}, "", nerdFile},
	} {
		if got := nerdEntryIcon(tc.e, tc.special); got != tc.want {
			t.Errorf("%s: nerdEntryIcon(%q) = %q, want %q", tc.desc, tc.e.Name, got, tc.want)
		}
	}
}

// TestIsTrashDir: a volume's .Trash and .Trash-$UID anywhere, the home
// trash only under .local/share.
func TestIsTrashDir(t *testing.T) {
	for _, tc := range []struct {
		dir, name string
		want      bool
	}{
		{"/media/usb", ".Trash-1000", true},
		{"/media/usb", ".Trash", true},
		{"/home/u/.local/share", "Trash", true},
		{"/home/u/.local/share/", "Trash", true},
		{"/home/u", "Trash", false},
		{"/home/u", ".Trashcan", false},
	} {
		if got := isTrashDir(tc.dir, tc.name); got != tc.want {
			t.Errorf("isTrashDir(%q, %q) = %v, want %v", tc.dir, tc.name, got, tc.want)
		}
	}
}

// iconTestModel returns a model whose config is saved in a temporary
// folder, its installed Nerd Font faked as font ("" for none).
func iconTestModel(t *testing.T, font string) *Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	old := findNerdFont
	t.Cleanup(func() { findNerdFont = old })
	findNerdFont = func() string { return font }
	return newTestModel()
}

var ctrlAltN = tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl | tea.ModAlt}

// TestToggleIconsWithoutNerdFont: with no Nerd Font installed, Ctrl+Alt+N
// says so and leaves the icons off.
func TestToggleIconsWithoutNerdFont(t *testing.T) {
	m := iconTestModel(t, "")
	m.Update(ctrlAltN)
	if m.dialog.Kind != DialogMessage || !strings.Contains(m.dialog.Message, "No Nerd Font found") {
		t.Fatalf("dialog = %v %q, want the no-Nerd-Font message", m.dialog.Kind, m.dialog.Message)
	}
	if m.cfg.NerdIcons {
		t.Error("icons turned on with no Nerd Font installed")
	}
}

// TestToggleIconsAsksFirst: with a Nerd Font installed, Ctrl+Alt+N warns
// about the layout and turns the icons on only when confirmed, saving the
// choice; pressed again, it turns them off without asking.
func TestToggleIconsAsksFirst(t *testing.T) {
	m := iconTestModel(t, "SymbolsNerdFont-Regular")

	m.Update(ctrlAltN)
	if m.dialog.Kind != DialogConfirmIcons {
		t.Fatalf("Ctrl+Alt+N opened dialog %v, want DialogConfirmIcons", m.dialog.Kind)
	}
	for _, want := range []string{"SymbolsNerdFont-Regular", "layout"} {
		if !strings.Contains(m.dialog.Message, want) {
			t.Errorf("the warning lacks %q: %q", want, m.dialog.Message)
		}
	}
	m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.dialog.Kind != DialogNone || m.cfg.NerdIcons {
		t.Fatalf("after n: dialog %v, icons %v; want closed and off", m.dialog.Kind, m.cfg.NerdIcons)
	}

	m.Update(ctrlAltN)
	m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.dialog.Kind != DialogNone || !m.cfg.NerdIcons {
		t.Fatalf("after y: dialog %v, icons %v; want closed and on", m.dialog.Kind, m.cfg.NerdIcons)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "shfm", "config.json"))
	if err != nil || !strings.Contains(string(data), `"nerd_icons": true`) {
		t.Errorf("config file doesn't record the icons on (err %v):\n%s", err, data)
	}

	m.Update(ctrlAltN)
	if m.dialog.Kind != DialogNone || m.cfg.NerdIcons {
		t.Errorf("second Ctrl+Alt+N: dialog %v, icons %v; want no dialog and off", m.dialog.Kind, m.cfg.NerdIcons)
	}
}

// TestRenderEntryLinesIcons: the list shows the bracketed icons by
// default and the Nerd Font ones when on, rows keeping the pane's width.
func TestRenderEntryLinesIcons(t *testing.T) {
	dir := t.TempDir()
	mkdirs(t, dir, "sub", ".Trash-1000")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	m := New(config.Default(), config.DefaultKeyMap(), Start{})
	m.cfg.ShowHidden = true
	p := NewPane(vfs.NewLocalFS("Local", dir), dir, true, 0, nil)

	row := func(lines []string, name string) string {
		for _, l := range lines {
			if strings.Contains(l, name) {
				return l
			}
		}
		t.Fatalf("no row for %s in %q", name, lines)
		return ""
	}
	const w = 50
	plain := m.renderEntryLines(0, p, w, 10)
	for name, icon := range map[string]string{"sub/": iconDir, "main.go": iconFile, "link/": iconDir} {
		if r := row(plain, name); !strings.Contains(r, icon) {
			t.Errorf("icons off: row %q lacks %q", r, icon)
		}
	}

	m.cfg.NerdIcons = true
	nerd := m.renderEntryLines(0, p, w, 10)
	for name, icon := range map[string]string{
		"sub/": nerdFolder, "main.go": "", "link/": nerdLinkDir, ".Trash-1000/": nerdTrash,
	} {
		r := row(nerd, name)
		if !strings.Contains(r, icon) {
			t.Errorf("icons on: row %q lacks %q", r, icon)
		}
		if got := lipgloss.Width(r); got != w {
			t.Errorf("icons on: row %q is %d cells wide, want %d", r, got, w)
		}
	}
}
