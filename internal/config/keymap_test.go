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

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultKeyMapActionFor(t *testing.T) {
	km := DefaultKeyMap()
	if a, ok := km.ActionFor("q"); !ok || a != ActionQuit {
		t.Errorf("ActionFor(q) = %v, %v; want %v, true", a, ok, ActionQuit)
	}
	if a, ok := km.ActionFor("ctrl+q"); !ok || a != ActionQuit {
		t.Errorf("ActionFor(ctrl+q) = %v, %v; want %v, true", a, ok, ActionQuit)
	}
	if _, ok := km.ActionFor("ctrl+z"); ok {
		t.Error("ActionFor(ctrl+z) should not match any default binding")
	}
}

func TestDefaultKeyMapCoversEveryAction(t *testing.T) {
	km := DefaultKeyMap()
	for _, d := range keybindingDefs {
		if len(km.KeysFor(d.action)) == 0 {
			t.Errorf("action %q has no default keys", d.action)
		}
	}
}

func TestParseKeyMapOverridesOnlyMentionedActions(t *testing.T) {
	data := []byte("quit = ctrl+x\n")
	km := parseKeyMap(data)

	if got := km.KeysFor(ActionQuit); len(got) != 1 || got[0] != "ctrl+x" {
		t.Errorf("quit keys = %v, want [ctrl+x]", got)
	}
	if a, ok := km.ActionFor("ctrl+x"); !ok || a != ActionQuit {
		t.Errorf("ActionFor(ctrl+x) = %v, %v; want %v, true", a, ok, ActionQuit)
	}
	// The old default key must no longer resolve to quit.
	if a, ok := km.ActionFor("q"); ok && a == ActionQuit {
		t.Error("ActionFor(q) should no longer be quit after rebinding")
	}
	// An action not mentioned in the file keeps its built-in default.
	if got := km.KeysFor(ActionHelp); len(got) == 0 {
		t.Error("help should keep its default keys when not mentioned in the file")
	}
}

func TestParseKeyMapDisablesActionWithEmptyKeys(t *testing.T) {
	km := parseKeyMap([]byte("empty-trash =\n"))
	if got := km.KeysFor(ActionEmptyTrash); got != nil {
		t.Errorf("empty-trash keys = %v, want none (disabled)", got)
	}
}

func TestParseKeyMapIgnoresComments(t *testing.T) {
	data := []byte("# quit = ctrl+z\nquit = ctrl+x  # my custom quit key\n")
	km := parseKeyMap(data)
	if a, ok := km.ActionFor("ctrl+z"); ok {
		t.Errorf("a commented-out line must not take effect, got action %v", a)
	}
	if got := km.KeysFor(ActionQuit); len(got) != 1 || got[0] != "ctrl+x" {
		t.Errorf("quit keys = %v, want [ctrl+x]", got)
	}
}

func TestParseKeyMapIgnoresUnknownAction(t *testing.T) {
	km := parseKeyMap([]byte("not-a-real-action = ctrl+z\n"))
	if _, ok := km.ActionFor("ctrl+z"); ok {
		t.Error("an unknown action's binding must not be applied")
	}
}

func TestParseKeyMapSpaceKeyword(t *testing.T) {
	km := parseKeyMap([]byte("toggle-select = space\n"))
	if a, ok := km.ActionFor(" "); !ok || a != ActionToggleSelect {
		t.Errorf("the literal space character should map to toggle-select, got %v, %v", a, ok)
	}
}

func TestParseKeyMapMultipleKeysAndMalformedLines(t *testing.T) {
	data := []byte("this line has no equals sign\ncursor-up = ctrl+up, up, k, w\n")
	km := parseKeyMap(data)
	got := km.KeysFor(ActionCursorUp)
	want := []string{"ctrl+up", "up", "k", "w"}
	if len(got) != len(want) {
		t.Fatalf("cursor-up keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cursor-up keys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriteDefaultKeymapFileThenParseRoundTrips(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "keybindings.conf")
	if err := writeDefaultKeymapFile(p); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "# shfm keybindings") {
		t.Error("the generated file should start with an explanatory comment")
	}
	got := parseKeyMap(data)
	want := DefaultKeyMap()
	for _, d := range keybindingDefs {
		g, w := got.KeysFor(d.action), want.KeysFor(d.action)
		if len(g) != len(w) {
			t.Errorf("%s: round-tripped keys = %v, want %v", d.action, g, w)
			continue
		}
		for i := range w {
			if g[i] != w[i] {
				t.Errorf("%s: round-tripped keys = %v, want %v", d.action, g, w)
				break
			}
		}
	}
	// Every description must fit the "at most 5 words" requirement.
	for _, d := range keybindingDefs {
		if n := len(strings.Fields(d.description)); n > 5 {
			t.Errorf("%s: description %q has %d words, want at most 5", d.action, d.description, n)
		}
	}
}

func TestLoadKeyMapCreatesFileWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	km := LoadKeyMap()
	if a, ok := km.ActionFor("q"); !ok || a != ActionQuit {
		t.Errorf("a freshly loaded keymap should have the defaults active, ActionFor(q) = %v, %v", a, ok)
	}

	p := filepath.Join(dir, "shfm", "keybindings.conf")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("keybindings.conf should have been created at %s: %v", p, err)
	}
}

func TestLoadKeyMapReusesExistingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	confDir := filepath.Join(dir, "shfm")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "keybindings.conf"), []byte("quit = ctrl+x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	km := LoadKeyMap()
	if a, ok := km.ActionFor("ctrl+x"); !ok || a != ActionQuit {
		t.Errorf("LoadKeyMap should have honored the existing file, ActionFor(ctrl+x) = %v, %v", a, ok)
	}
}

func TestMirrorDefaultKey(t *testing.T) {
	km := DefaultKeyMap()
	if a, ok := km.ActionFor("alt+ctrl+s"); !ok || a != ActionMirror {
		t.Fatalf("alt+ctrl+s -> %q, %v; want %q", a, ok, ActionMirror)
	}
	if a, _ := km.ActionFor("ctrl+s"); a != ActionSourceMenu {
		t.Fatalf("ctrl+s -> %q, want the source menu", a)
	}
}
