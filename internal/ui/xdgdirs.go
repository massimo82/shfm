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
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"shfm/internal/vfs"
)

// xdgUserDirDefaults gives the fallback path (relative to $HOME) for each of
// the standard XDG_xxx_DIR variables defined by the freedesktop.org
// xdg-user-dirs spec (see user-dirs.dirs(5)) — the folders every desktop
// file manager treats as "special" (Desktop, Documents, Downloads, ...).
// Used when user-dirs.dirs doesn't exist, or doesn't list a given variable.
var xdgUserDirDefaults = map[string]string{
	"XDG_DESKTOP_DIR":     "Desktop",
	"XDG_DOWNLOAD_DIR":    "Downloads",
	"XDG_TEMPLATES_DIR":   "Templates",
	"XDG_PUBLICSHARE_DIR": "Public",
	"XDG_DOCUMENTS_DIR":   "Documents",
	"XDG_MUSIC_DIR":       "Music",
	"XDG_PICTURES_DIR":    "Pictures",
	"XDG_VIDEOS_DIR":      "Videos",
}

var userDirsLineRe = regexp.MustCompile(`^(XDG_[A-Z]+_DIR)\s*=\s*"(.*)"\s*$`)

// xdgUserDirs maps the absolute paths of the current user's standard XDG
// user directories, for the given home folder, to their user-dirs.dirs
// key ("XDG_MUSIC_DIR"...). It honors
// $XDG_CONFIG_HOME/user-dirs.dirs (or ~/.config/user-dirs.dirs when
// XDG_CONFIG_HOME is unset) when present — that's where xdg-user-dirs-update
// records any localized names (e.g. "Documenti" on an Italian system) or
// relocated folders — and falls back to the spec's own default layout for
// any variable the file doesn't list, or if it doesn't exist at all. Not
// cached: called only when actually browsing home (see xdgEntryNames), so
// the one extra file read is negligible, and it means a change to
// user-dirs.dirs takes effect immediately rather than needing a restart.
func xdgUserDirs(home string) map[string]string {
	values := make(map[string]string, len(xdgUserDirDefaults))
	for k, v := range xdgUserDirDefaults {
		values[k] = filepath.Join(home, v)
	}

	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		configDir = filepath.Join(home, ".config")
	}
	if f, err := os.Open(filepath.Join(configDir, "user-dirs.dirs")); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			m := userDirsLineRe.FindStringSubmatch(strings.TrimSpace(sc.Text()))
			if m == nil {
				continue
			}
			if _, known := xdgUserDirDefaults[m[1]]; !known {
				continue
			}
			path := strings.ReplaceAll(m[2], "$HOME", home)
			if !filepath.IsAbs(path) {
				continue
			}
			values[m[1]] = filepath.Clean(path)
		}
	}

	dirs := make(map[string]string, len(values))
	for key, path := range values {
		dirs[path] = key
	}
	return dirs
}

// xdgEntryNames maps, from among entries, the Names that are the user's
// standard XDG user directories to their user-dirs.dirs key — only when fs is the local
// filesystem and dir is the user's home folder, since that's the only place
// those well-known folders actually live; a folder that merely happens to
// be named e.g. "Documents" somewhere else in the tree isn't treated
// specially. Returns nil when none apply, so callers can fall back to plain
// dirs-first sorting.
func xdgEntryNames(fs vfs.FileSystem, dir string, entries []vfs.Entry) map[string]string {
	if fs.Kind() != vfs.KindLocal {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil || filepath.Clean(dir) != filepath.Clean(home) {
		return nil
	}
	xdgPaths := xdgUserDirs(home)
	var names map[string]string
	for _, e := range entries {
		key := xdgPaths[fs.Join(dir, e.Name)]
		if !e.IsDir || key == "" {
			continue
		}
		if names == nil {
			names = map[string]string{}
		}
		names[e.Name] = key
	}
	return names
}
