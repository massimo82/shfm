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

	"shfm/internal/pick"
)

// Start says where shfm opens, from its command line (see main.go).
type Start struct {
	// Paths are local paths or file:// URIs: a folder to open, or files
	// to show selected in their folder (the first one's; those elsewhere
	// are ignored).
	Paths []string
	// Select shows every path as an item of its folder, folders
	// included, instead of opening a lone folder (FileManager1's
	// ShowItems, see internal/filemanager1).
	Select bool
	// Properties also opens the properties of the first path (implies
	// Select).
	Properties bool
	// Pick runs shfm as a file chooser (see picker.go).
	Pick *pick.Request
}

// resolveStart returns the folder to open both panes on and the names, in
// it, to put the cursor on and select. Anything invalid (missing,
// unresolvable) falls back, rather than refusing to start over a typo'd
// or since deleted path: to the item's folder if that still exists, to
// homeOrRoot otherwise.
func resolveStart(s Start) (dir string, names []string) {
	var paths []string
	for _, arg := range s.Paths {
		if p, ok := startArgPath(arg); ok {
			paths = append(paths, p)
		}
	}
	if s.Pick != nil {
		paths, s.Select = pickStartPaths(*s.Pick)
	}
	if len(paths) == 0 {
		return homeOrRoot(), nil
	}
	first := paths[0]
	info, err := os.Stat(first)
	if err == nil && info.IsDir() && len(paths) == 1 && !s.Select && !s.Properties {
		return first, nil
	}
	dir = filepath.Dir(first)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return homeOrRoot(), nil
	}
	for _, p := range paths {
		if filepath.Dir(p) != dir || p == dir {
			continue
		}
		if _, err := os.Lstat(p); err == nil {
			names = append(names, filepath.Base(p))
		}
	}
	return dir, names
}

// startArgPath turns a command line argument into an absolute local path:
// relative paths are taken from the current folder, and file:// URIs (as
// desktop launchers pass them) are decoded.
func startArgPath(arg string) (string, bool) {
	if strings.Contains(arg, "://") {
		scheme, rest, ok := decodeURI(arg)
		if !ok || scheme != "file" {
			return "", false
		}
		// "file:///p" leaves "/p"; "file://localhost/p", "localhost/p".
		rest = strings.TrimPrefix(rest, "localhost")
		if !strings.HasPrefix(rest, "/") {
			return "", false
		}
		return filepath.Clean(rest), true
	}
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", false
	}
	return abs, true
}

// reveal puts the active pane's cursor on the first of names and, when
// there are several, selects them all.
func (m *Model) reveal(names []string) {
	if len(names) == 0 {
		return
	}
	p := m.activePane()
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
		// A hidden file asked for by name is shown: in this pane, and
		// without changing the saved preference.
		if strings.HasPrefix(n, ".") && !p.ShowHidden {
			p.ShowHidden = true
			p.Load()
		}
	}
	cursorSet := false
	for i, e := range p.Entries {
		if !want[e.Name] || IsParentEntry(e) {
			continue
		}
		if !cursorSet {
			p.Cursor, cursorSet = i, true
		}
		if len(names) > 1 {
			p.Selected[e.Name] = true
		}
	}
}
