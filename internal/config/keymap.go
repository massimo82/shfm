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
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Action identifies one configurable shfm shortcut. The string value is
// also its name in the keybindings file, so it must stay stable across
// releases — renaming one silently drops any customization a user made for
// it (it just falls back to the default, since an unknown name in the file
// is ignored rather than erroring).
type Action string

const (
	ActionQuit             Action = "quit"
	ActionHelp             Action = "help"
	ActionToggleLayout     Action = "toggle-layout"
	ActionTaskList         Action = "task-list"
	ActionCursorUp         Action = "cursor-up"
	ActionCursorDown       Action = "cursor-down"
	ActionPaneLeft         Action = "pane-left"
	ActionPaneRight        Action = "pane-right"
	ActionSwitchPane       Action = "switch-pane"
	ActionPageUp           Action = "page-up"
	ActionPageDown         Action = "page-down"
	ActionGoTop            Action = "go-top"
	ActionGoBottom         Action = "go-bottom"
	ActionActivate         Action = "activate"
	ActionGoUp             Action = "go-up"
	ActionToggleSelect     Action = "toggle-select"
	ActionSelectAll        Action = "select-all"
	ActionDeselectAll      Action = "deselect-all"
	ActionCopy             Action = "copy"
	ActionPaste            Action = "paste"
	ActionPasteMove        Action = "paste-move"
	ActionTrash            Action = "trash"
	ActionDelete           Action = "delete"
	ActionRename           Action = "rename"
	ActionNewFolder        Action = "new-folder"
	ActionNewFile          Action = "new-file"
	ActionProperties       Action = "properties"
	ActionSearch           Action = "search"
	ActionSemanticSearch   Action = "semantic-search"
	ActionCancel           Action = "cancel"
	ActionSourceMenu       Action = "source-menu"
	ActionEditPath         Action = "edit-path"
	ActionToggleTrashView  Action = "toggle-trash-view"
	ActionRestoreOrRefresh Action = "restore-or-refresh"
	ActionEmptyTrash       Action = "empty-trash"
	ActionMirror           Action = "mirror"
)

// keybindingDef is one entry in the fixed, built-in catalogue of
// configurable actions: its default keys and a short (<=5 words)
// description, used both as the comment written next to it in a freshly
// created keybindings file and as the description shown in the Help
// dialog — the two are always the same text, by construction.
type keybindingDef struct {
	action      Action
	defaultKeys []string
	description string
}

// keybindingDefs is the full catalogue, in display order (this order is
// also what the Help dialog and a freshly written keybindings file use).
var keybindingDefs = []keybindingDef{
	{ActionQuit, []string{"q", "ctrl+q"}, "Quit the application"},
	{ActionHelp, []string{"alt+ctrl+h", "?"}, "Show keyboard shortcuts help"},
	{ActionToggleLayout, []string{"ctrl+l"}, "Toggle single or dual pane"},
	{ActionTaskList, []string{"ctrl+b"}, "Show background tasks list"},

	{ActionCursorUp, []string{"ctrl+up", "up", "k"}, "Move cursor up"},
	{ActionCursorDown, []string{"ctrl+down", "down", "j"}, "Move cursor down"},
	{ActionPaneLeft, []string{"left"}, "Switch to left pane"},
	{ActionPaneRight, []string{"right"}, "Switch to right pane"},
	{ActionSwitchPane, []string{"tab"}, "Switch active pane"},
	{ActionPageUp, []string{"pgup"}, "Move cursor one page up"},
	{ActionPageDown, []string{"pgdown"}, "Move cursor one page down"},
	{ActionGoTop, []string{"home", "g"}, "Jump to first entry"},
	{ActionGoBottom, []string{"end", "G"}, "Jump to last entry"},
	{ActionActivate, []string{"enter"}, "Open folder or file"},
	{ActionGoUp, []string{"backspace", "h"}, "Go up one folder"},

	{ActionToggleSelect, []string{" "}, "Toggle selection of entry"},
	{ActionSelectAll, []string{"a"}, "Select all entries"},
	{ActionDeselectAll, []string{"A"}, "Deselect all entries"},

	{ActionCopy, []string{"ctrl+c"}, "Copy selection to clipboard"},
	{ActionPaste, []string{"ctrl+v"}, "Paste clipboard as copy"},
	{ActionPasteMove, []string{"alt+ctrl+v"}, "Paste clipboard as move"},
	{ActionTrash, []string{"ctrl+d"}, "Move selection to trash"},
	{ActionDelete, []string{"alt+ctrl+d"}, "Permanently delete selection"},
	{ActionMirror, []string{"alt+ctrl+s"}, "Paste as mirror, or list"},

	{ActionRename, []string{"r"}, "Rename current entry"},
	{ActionNewFolder, []string{"m"}, "Create new folder"},
	{ActionNewFile, []string{"f"}, "Create new file"},
	{ActionProperties, []string{"i"}, "Show entry properties"},

	{ActionSearch, []string{"/"}, "Search or filter by name"},
	{ActionSemanticSearch, []string{"ctrl+f"}, "Search file contents semantically"},
	{ActionCancel, []string{"esc"}, "Cancel search or filter"},

	{ActionSourceMenu, []string{"ctrl+s"}, "Open source picker"},
	{ActionEditPath, []string{"ctrl+p"}, "Edit current path"},

	{ActionToggleTrashView, []string{"T"}, "Toggle trash view"},
	{ActionRestoreOrRefresh, []string{"R"}, "Restore item or refresh"},
	{ActionEmptyTrash, []string{"e"}, "Empty the trash"},
}

// KeyMap maps each Action to the keys that trigger it (as bubbletea's
// tea.KeyMsg.String() would spell them, e.g. "ctrl+up", "a", "alt+ctrl+h"),
// plus the reverse lookup handleKey actually uses on every keypress.
type KeyMap struct {
	bindings map[Action][]string
	reverse  map[string]Action
}

// DefaultKeyMap returns the built-in keybindings, unmodified.
func DefaultKeyMap() *KeyMap {
	km := &KeyMap{bindings: map[Action][]string{}}
	for _, d := range keybindingDefs {
		km.bindings[d.action] = append([]string(nil), d.defaultKeys...)
	}
	km.rebuildReverse()
	return km
}

func (km *KeyMap) rebuildReverse() {
	km.reverse = map[string]Action{}
	for action, keys := range km.bindings {
		for _, k := range keys {
			km.reverse[k] = action
		}
	}
}

// ActionFor returns the action bound to key (as tea.KeyMsg.String() would
// spell it), if any.
func (km *KeyMap) ActionFor(key string) (Action, bool) {
	a, ok := km.reverse[key]
	return a, ok
}

// KeysFor returns the keys currently bound to action, in the order they'll
// be displayed (empty if the user disabled it entirely).
func (km *KeyMap) KeysFor(action Action) []string {
	return km.bindings[action]
}

// KeyBinding pairs one catalogue entry with its currently active keys, for
// building the Help dialog from the same source of truth as ActionFor.
type KeyBinding struct {
	Action      Action
	Description string
	Keys        []string
}

// All returns every configurable action, in catalogue order, with its
// currently active keys — always consistent with whatever ActionFor uses,
// since both read from the same KeyMap.
func (km *KeyMap) All() []KeyBinding {
	out := make([]KeyBinding, len(keybindingDefs))
	for i, d := range keybindingDefs {
		out[i] = KeyBinding{Action: d.action, Description: d.description, Keys: km.bindings[d.action]}
	}
	return out
}

func isKnownAction(a Action) bool {
	for _, d := range keybindingDefs {
		if d.action == a {
			return true
		}
	}
	return false
}

// displayKeyName/internalKeyName translate the space bar's key to/from a
// readable name for the keybindings file — tea.KeyMsg.String() for it is
// literally " ", invisible and error-prone to hand-edit.
func displayKeyName(k string) string {
	if k == " " {
		return "space"
	}
	return k
}

func internalKeyName(k string) string {
	if k == "space" {
		return " "
	}
	return k
}

func keymapPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	dir = filepath.Join(dir, "shfm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "keybindings.conf"), nil
}

// LoadKeyMap loads keybindings.conf, if present, layering it over the
// built-in defaults (an action the file doesn't mention, or an unknown
// name, keeps/is ignored in favour of its default — see parseKeyMap).
// If the file doesn't exist yet, it's created now with every default
// binding and a short comment explaining each one, and the defaults are
// used for this run: shfm always works out of the box, the file is there
// from the first run onward purely so there's something obvious to edit.
func LoadKeyMap() *KeyMap {
	p, err := keymapPath()
	if err != nil {
		return DefaultKeyMap()
	}
	data, err := os.ReadFile(p)
	if err != nil {
		km := DefaultKeyMap()
		_ = writeDefaultKeymapFile(p) // best-effort: a read-only config dir shouldn't block startup
		return km
	}
	return parseKeyMap(data)
}

// parseKeyMap reads a keybindings.conf: one binding per non-comment,
// non-blank line, "<action> = <key>[, <key>...]  # comment". A line whose
// action name isn't in the built-in catalogue, or that has no "=", is
// ignored outright (forward/backward compatible with other shfm versions'
// files); an action given with nothing after "=" is bound to zero keys,
// i.e. disabled. Every action the file doesn't mention at all keeps its
// built-in default.
func parseKeyMap(data []byte) *KeyMap {
	km := DefaultKeyMap()
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		action := Action(strings.TrimSpace(line[:eq]))
		if !isKnownAction(action) {
			continue
		}
		rest := line[eq+1:]
		if h := strings.IndexByte(rest, '#'); h >= 0 {
			rest = rest[:h]
		}
		var keys []string
		for _, part := range strings.Split(rest, ",") {
			if k := strings.TrimSpace(part); k != "" {
				keys = append(keys, internalKeyName(k))
			}
		}
		km.bindings[action] = keys
	}
	km.rebuildReverse()
	return km
}

// writeDefaultKeymapFile writes every built-in binding to p, one per line,
// each with its description as a trailing comment — the file a fresh
// install (or a deleted keybindings.conf) gets on the next startup.
func writeDefaultKeymapFile(p string) error {
	var b strings.Builder
	b.WriteString("# shfm keybindings\n#\n")
	b.WriteString("# One shortcut per line: <action> = <key>[, <key>...]  # description\n")
	b.WriteString("# Comment lines start with '#'. To disable a shortcut, remove the keys\n")
	b.WriteString("# after its '=' (leave it blank). Unknown actions and malformed lines are\n")
	b.WriteString("# ignored, so a mistyped line is simply skipped rather than breaking shfm.\n")
	b.WriteString("# The space bar is written as \"space\". Restart shfm after editing this file.\n\n")

	const actionWidth = 20
	for _, d := range keybindingDefs {
		keys := make([]string, len(d.defaultKeys))
		for i, k := range d.defaultKeys {
			keys[i] = displayKeyName(k)
		}
		assignment := fmt.Sprintf("%-*s = %s", actionWidth, d.action, strings.Join(keys, ", "))
		fmt.Fprintf(&b, "%-45s # %s\n", assignment, d.description)
	}
	return os.WriteFile(p, []byte(b.String()), 0o644)
}
