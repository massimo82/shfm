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

package opener

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"shfm/internal/toolpath"
)

// Installed applications, per the freedesktop.org Desktop Entry
// specification: every .desktop file under each $XDG_DATA_DIRS/
// applications, subfolders included, named by its desktop file ID.

func appDirs() []string {
	var dirs []string
	for _, d := range dataDirs() {
		dirs = append(dirs, filepath.Join(d, "applications"))
	}
	return dirs
}

// currentDesktops returns $XDG_CURRENT_DESKTOP's names ("GNOME", "KDE",
// ...), in its order.
func currentDesktops() []string {
	var out []string
	for _, d := range strings.Split(os.Getenv("XDG_CURRENT_DESKTOP"), ":") {
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// Listed reports whether app belongs in a list of applications to choose
// from: not NoDisplay, and shown in the current desktop (OnlyShowIn,
// NotShowIn). An unlisted app is still installed, and still valid as a
// type's default.
func (app App) Listed() bool {
	if app.NoDisplay {
		return false
	}
	desktops := currentDesktops()
	for _, d := range desktops {
		if containsString(app.NotShowIn, d) {
			return false
		}
	}
	if len(app.OnlyShowIn) == 0 {
		return true
	}
	for _, d := range desktops {
		if containsString(app.OnlyShowIn, d) {
			return true
		}
	}
	return false
}

// installedApps returns every installed application by desktop file ID.
// The same ID in a higher-priority directory hides the lower ones — even
// when that entry is Hidden or invalid, which is how a user deletes a
// system application for themselves.
func installedApps() map[string]App {
	apps := map[string]App{}
	seen := map[string]bool{}
	for i, dir := range appDirs() {
		_ = filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.HasSuffix(path, ".desktop") {
				return nil
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return nil
			}
			id := strings.ReplaceAll(filepath.ToSlash(rel), "/", "-")
			if seen[id] {
				return nil
			}
			seen[id] = true
			if app, ok := parseDesktopFile(path); ok {
				app.ID, app.dir = id, i
				apps[id] = app
			}
			return nil
		})
	}
	return apps
}

// ListApps returns the installed applications to choose from (see
// Listed), sorted by name — read afresh each time, so it follows what's
// installed or removed while shfm runs.
func ListApps() []App {
	var apps []App
	for _, app := range installedApps() {
		if app.Listed() {
			apps = append(apps, app)
		}
	}
	sort.Slice(apps, func(i, j int) bool {
		a, b := strings.ToLower(apps[i].Name), strings.ToLower(apps[j].Name)
		if a != b {
			return a < b
		}
		return apps[i].ID < apps[j].ID
	})
	return apps
}

// parseDesktopFile reads an application's desktop entry: false when it
// isn't a launchable one (not Type=Application, Hidden, no Exec, or a
// TryExec that isn't installed).
func parseDesktopFile(path string) (App, bool) {
	lines, err := readLines(path)
	if err != nil {
		return App{}, false
	}
	values := map[string]string{}
	inEntry := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if inEntry {
				break // only the first group is the entry; actions follow
			}
			inEntry = line == "[Desktop Entry]"
			continue
		}
		if !inEntry {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		if _, dup := values[key]; !dup {
			values[key] = strings.TrimSpace(val)
		}
	}
	if values["Type"] != "Application" || values["Hidden"] == "true" || values["Exec"] == "" {
		return App{}, false
	}
	if try := unescapeValue(values["TryExec"]); try != "" && !executable(try) {
		return App{}, false
	}
	app := App{
		ID:         filepath.Base(path),
		Name:       unescapeValue(localized(values, "Name")),
		Exec:       unescapeValue(values["Exec"]),
		Icon:       unescapeValue(localized(values, "Icon")),
		WorkDir:    unescapeValue(values["Path"]),
		File:       path,
		Terminal:   values["Terminal"] == "true",
		NoDisplay:  values["NoDisplay"] == "true",
		MimeTypes:  splitList(values["MimeType"]),
		OnlyShowIn: splitList(values["OnlyShowIn"]),
		NotShowIn:  splitList(values["NotShowIn"]),
	}
	if app.Name == "" {
		app.Name = strings.TrimSuffix(filepath.Base(path), ".desktop")
	}
	return app, true
}

// executable reports whether a TryExec names an installed program.
func executable(name string) bool {
	_, err := toolpath.Find(name)
	return err == nil
}

// localized returns key's value for the user's language — the spec's
// lang_COUNTRY@MODIFIER, lang_COUNTRY, lang@MODIFIER, lang order, from
// LC_ALL, LC_MESSAGES or LANG — or its untranslated one.
func localized(values map[string]string, key string) string {
	for _, loc := range localeVariants() {
		if v, ok := values[key+"["+loc+"]"]; ok && v != "" {
			return v
		}
	}
	return values[key]
}

func localeVariants() []string {
	var locale string
	for _, env := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if locale = os.Getenv(env); locale != "" {
			break
		}
	}
	if locale == "" || locale == "C" || locale == "POSIX" || strings.HasPrefix(locale, "C.") {
		return nil
	}
	rest, modifier, _ := strings.Cut(locale, "@")
	rest, _, _ = strings.Cut(rest, ".") // the encoding plays no part
	lang, country, _ := strings.Cut(rest, "_")
	var out []string
	if country != "" && modifier != "" {
		out = append(out, lang+"_"+country+"@"+modifier)
	}
	if country != "" {
		out = append(out, lang+"_"+country)
	}
	if modifier != "" {
		out = append(out, lang+"@"+modifier)
	}
	return append(out, lang)
}

// unescapeValue undoes a desktop entry string's escapes (\s \n \t \r \\).
func unescapeValue(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// splitList splits a desktop entry list value on its unescaped ';'.
func splitList(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if v := strings.TrimSpace(unescapeValue(cur.String())); v != "" {
			out = append(out, v)
		}
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == ';':
			cur.WriteByte(';')
			i++
		case s[i] == ';':
			flush()
		default:
			cur.WriteByte(s[i])
		}
	}
	flush()
	return out
}
