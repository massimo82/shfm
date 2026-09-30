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

// Package nerdfont tells whether a Nerd Font (https://www.nerdfonts.com/)
// is installed, before shfm shows its icons. It can't know the terminal's
// font, which may even be on another machine over SSH: a Nerd Font on the
// system is the best hint that the icons will show up.
package nerdfont

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"shfm/internal/toolpath"
)

// fontDirs, findTool and runTool are variables so tests can fake the font
// folders and fontconfig.
var (
	fontDirs = defaultFontDirs
	findTool = toolpath.Find
	runTool  = func(path string, args ...string) ([]byte, error) {
		return exec.Command(path, args...).Output()
	}
)

// Find returns the name of an installed Nerd Font, "" if there's none.
// It looks in the font folders fontconfig reads by default first, then
// asks fc-list, when installed, for the fonts its configuration adds.
func Find() string {
	for _, dir := range fontDirs() {
		if name := findInDir(dir); name != "" {
			return name
		}
	}
	return findWithFontconfig()
}

// defaultFontDirs lists the font folders of the XDG base directories, the
// legacy ~/.fonts included.
func defaultFontDirs() []string {
	var dirs []string
	home, _ := os.UserHomeDir()
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" && home != "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	if dataHome != "" {
		dirs = append(dirs, filepath.Join(dataHome, "fonts"))
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".fonts"))
	}
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	for _, d := range filepath.SplitList(dataDirs) {
		if d != "" {
			dirs = append(dirs, filepath.Join(d, "fonts"))
		}
	}
	return dirs
}

// findInDir walks dir for a font file whose name marks a Nerd Font:
// "JetBrainsMonoNerdFont-Regular.ttf", "SymbolsNerdFontMono-Regular.ttf",
// or older releases' "Hack Regular Nerd Font Complete.ttf".
func findInDir(dir string) string {
	var found string
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		switch strings.ToLower(filepath.Ext(name)) {
		case ".ttf", ".otf", ".ttc", ".otc":
		default:
			return nil
		}
		base := strings.TrimSuffix(name, filepath.Ext(name))
		if isNerdFontName(base) {
			found = base
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// findWithFontconfig looks for a Nerd Font family among those fc-list
// knows. No fc-list, or its failure, means none found.
func findWithFontconfig() string {
	path, err := findTool("fc-list")
	if err != nil {
		return ""
	}
	out, err := runTool(path, ":", "family")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		// A font with several family names lists them comma-separated.
		for _, family := range strings.Split(line, ",") {
			if family = strings.TrimSpace(family); isNerdFontName(family) {
				return family
			}
		}
	}
	return ""
}

// isNerdFontName tells whether a font's file or family name marks it as
// a Nerd Font, however spaces, dashes and case are written.
func isNerdFontName(name string) bool {
	n := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(name))
	return strings.Contains(n, "nerdfont")
}
