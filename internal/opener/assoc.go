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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Which application opens each type, per the freedesktop.org MIME
// Applications Associations specification — the mimeapps.list files GNOME
// (GIO), KDE and the other desktops all read and write, so a choice made
// here holds in them too, and theirs here.

const (
	sectionDefault = "Default Applications"
	sectionAdded   = "Added Associations"
	sectionRemoved = "Removed Associations"
)

// Association is one file type, the extensions that make a file that
// type, and the application opening it.
type Association struct {
	MimeType   string
	Extensions []string // e.g. ".jpg", ".jpeg", from the Shared MIME-info database; may be empty
	App        App      // valid when HasApp
	HasApp     bool
	// User is set when the user chose the application (a default in one
	// of their own mimeapps.list): ResetAssociation puts the system's
	// choice back.
	User bool
}

// iniSections is a parsed mimeapps.list/mimeinfo.cache: section → MIME
// type → its desktop file IDs, in order.
type iniSections map[string]map[string][]string

func loadIni(path string, db *mimeDB) iniSections {
	lines, err := readLines(path)
	if err != nil {
		return nil
	}
	out := iniSections{}
	section := ""
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		if db != nil {
			key = db.unalias(key)
		}
		if out[section] == nil {
			out[section] = map[string][]string{}
		}
		for _, id := range splitList(val) {
			if !containsString(out[section][key], id) {
				out[section][key] = append(out[section][key], id)
			}
		}
	}
	return out
}

// mimeappsLayer is one association source: a mimeapps.list, or the
// applications installed in an applications directory (declared: the
// types they declare are associated with them, as its mimeinfo.cache
// would list — read from the entries themselves, as GIO and KDE do, so a
// stale or missing cache doesn't matter).
type mimeappsLayer struct {
	path     string
	sections iniSections
	declared bool
	dir      int  // declared: index in appDirs
	user     bool // the user's own file
}

// mimeappsFiles lists every association source, highest precedence first,
// as the spec orders them: in each config directory, then each
// applications directory (deprecated there, still read), the current
// desktops' $desktop-mimeapps.list, then mimeapps.list — and each
// applications directory's own applications after its lists.
func mimeappsFiles() []mimeappsLayer {
	var desktops []string
	for _, d := range currentDesktops() {
		desktops = append(desktops, strings.ToLower(d)+"-mimeapps.list")
	}
	names := append(desktops, "mimeapps.list")
	var out []mimeappsLayer
	for i, dir := range configDirs() {
		for _, n := range names {
			out = append(out, mimeappsLayer{path: filepath.Join(dir, n), user: i == 0})
		}
	}
	for i, dir := range appDirs() {
		for _, n := range names {
			out = append(out, mimeappsLayer{path: filepath.Join(dir, n), user: i == 0})
		}
		out = append(out, mimeappsLayer{declared: true, dir: i})
	}
	return out
}

// assocIndex holds every association source and installed application,
// read once, to resolve many types at a time.
type assocIndex struct {
	layers   []mimeappsLayer
	apps     map[string]App
	declared map[int]map[string][]string // applications directory → type → IDs declaring it, sorted
	db       *mimeDB
}

func loadAssocIndex() *assocIndex {
	idx := &assocIndex{apps: installedApps(), declared: map[int]map[string][]string{}, db: loadMimeDB()}
	for _, app := range idx.apps {
		if idx.declared[app.dir] == nil {
			idx.declared[app.dir] = map[string][]string{}
		}
		for _, t := range app.MimeTypes {
			t = idx.db.unalias(t)
			if !containsString(idx.declared[app.dir][t], app.ID) {
				idx.declared[app.dir][t] = append(idx.declared[app.dir][t], app.ID)
			}
		}
	}
	for _, byType := range idx.declared {
		for _, ids := range byType {
			sort.Strings(ids)
		}
	}
	for _, l := range mimeappsFiles() {
		if l.declared {
			idx.layers = append(idx.layers, l)
		} else if l.sections = loadIni(l.path, idx.db); l.sections != nil {
			idx.layers = append(idx.layers, l)
		}
	}
	return idx
}

// userDefault reports whether one of the user's own lists sets a default
// for mimeType.
func (idx *assocIndex) userDefault(mimeType string) bool {
	for _, l := range idx.layers {
		if l.user && len(l.sections[sectionDefault][mimeType]) > 0 {
			return true
		}
	}
	return false
}

// defaultFor returns the first installed application in mimeType's
// [Default Applications] entries, in precedence order.
func (idx *assocIndex) defaultFor(mimeType string) (App, bool) {
	for _, l := range idx.layers {
		for _, id := range l.sections[sectionDefault][mimeType] {
			if app, ok := idx.apps[id]; ok {
				return app, true
			}
		}
	}
	return App{}, false
}

// associated returns the installed applications associated with
// mimeType, most preferred first: each source's [Added Associations]
// (the applications declaring the type, for a directory's own), less what
// a higher-precedence source's [Removed Associations] took away.
func (idx *assocIndex) associated(mimeType string) []App {
	var out []App
	seen, removed := map[string]bool{}, map[string]bool{}
	for _, l := range idx.layers {
		ids := l.sections[sectionAdded][mimeType]
		if l.declared {
			ids = idx.declared[l.dir][mimeType]
		}
		for _, id := range ids {
			if app, ok := idx.apps[id]; ok && !seen[id] && !removed[id] {
				seen[id] = true
				out = append(out, app)
			}
		}
		for _, id := range l.sections[sectionRemoved][mimeType] {
			removed[id] = true
		}
	}
	return out
}

// resolve returns the application that opens mimeType: its default, else
// its most preferred associated application — trying, when the type has
// neither, its supertypes (a C source file opens with a text editor).
func (idx *assocIndex) resolve(mimeType string) (App, bool) {
	types := append([]string{idx.db.unalias(mimeType)}, idx.db.ancestors(mimeType)...)
	for _, t := range types {
		if app, ok := idx.defaultFor(t); ok {
			return app, true
		}
	}
	for _, t := range types {
		if apps := idx.associated(t); len(apps) > 0 {
			return apps[0], true
		}
	}
	return App{}, false
}

// Associations lists every file type with an extension that some
// application is associated with, plus every type the user set a default
// for, sorted by extension (types with none last), each resolved as
// DefaultApp would.
func Associations() []Association {
	idx := loadAssocIndex()
	types := map[string]bool{}
	for _, l := range idx.layers {
		for _, section := range []string{sectionDefault, sectionAdded} {
			for t := range l.sections[section] {
				types[t] = true
			}
		}
	}
	for _, byType := range idx.declared {
		for t := range byType {
			if !strings.HasSuffix(t, "/*") {
				types[t] = true
			}
		}
	}
	var out []Association
	for t := range types {
		a := Association{MimeType: t, User: idx.userDefault(t), Extensions: Extensions(t)}
		if len(a.Extensions) == 0 && !a.User {
			continue // not a file type one names by extension (URL schemes, ...)
		}
		a.App, a.HasApp = idx.resolve(t)
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		ei, ej := out[i].Extensions, out[j].Extensions
		if (len(ei) == 0) != (len(ej) == 0) {
			return len(ej) == 0
		}
		if len(ei) > 0 {
			if a, b := strings.ToLower(ei[0]), strings.ToLower(ej[0]); a != b {
				return a < b
			}
		}
		return out[i].MimeType < out[j].MimeType
	})
	return out
}

// CanOpen reports whether app declares mimeType, directly or through a
// "type/*" wildcard.
func (app App) CanOpen(mimeType string) bool {
	major, _, _ := strings.Cut(mimeType, "/")
	for _, t := range app.MimeTypes {
		if t == mimeType || t == major+"/*" {
			return true
		}
	}
	return false
}

// SaveDefaultApp makes app the default for mimeType, as GNOME and KDE do:
// in the user's $XDG_CONFIG_HOME/mimeapps.list, the [Default
// Applications] entry, app first in [Added Associations], and app out of
// [Removed Associations]. A default for the type in the user's other lists
// (a $desktop-mimeapps.list, which would take precedence) is dropped.
func SaveDefaultApp(mimeType string, app App) error {
	main, err := userMimeappsPath()
	if err != nil {
		return err
	}
	for _, path := range userMimeappsLists() {
		if path == main {
			continue
		}
		if err := editMimeapps(path, func(lines []string) []string {
			return setAssociation(lines, sectionDefault, mimeType, nil)
		}); err != nil {
			return err
		}
	}
	return editMimeapps(main, func(lines []string) []string {
		lines = setAssociation(lines, sectionDefault, mimeType, []string{app.ID})
		added := []string{app.ID}
		for _, id := range getAssociation(lines, sectionAdded, mimeType) {
			if id != app.ID {
				added = append(added, id)
			}
		}
		lines = setAssociation(lines, sectionAdded, mimeType, added)
		var removed []string
		for _, id := range getAssociation(lines, sectionRemoved, mimeType) {
			if id != app.ID {
				removed = append(removed, id)
			}
		}
		return setAssociation(lines, sectionRemoved, mimeType, removed)
	})
}

// ResetAssociation drops every choice the user made for mimeType from
// their own lists — its default, added and removed associations, as
// GNOME's "Reset" does — so the system's choice applies again.
func ResetAssociation(mimeType string) error {
	for _, path := range userMimeappsLists() {
		if err := editMimeapps(path, func(lines []string) []string {
			for _, s := range []string{sectionDefault, sectionAdded, sectionRemoved} {
				lines = setAssociation(lines, s, mimeType, nil)
			}
			return lines
		}); err != nil {
			return err
		}
	}
	return nil
}

func userMimeappsPath() (string, error) {
	dirs := configDirs()
	if len(dirs) == 0 {
		return "", fmt.Errorf("no config directory available")
	}
	return filepath.Join(dirs[0], "mimeapps.list"), nil
}

// userMimeappsLists are the user's own association lists.
func userMimeappsLists() []string {
	var out []string
	for _, l := range mimeappsFiles() {
		if l.user && !l.declared {
			out = append(out, l.path)
		}
	}
	return out
}

// editMimeapps applies edit to the mimeapps.list at path, writing it back
// (atomically, keeping everything else in it as it was) only when that
// changed it. A missing file is created, when there's something to write.
func editMimeapps(path string, edit func([]string) []string) error {
	lines, err := readLines(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	before := strings.Join(lines, "\n")
	lines = edit(lines)
	after := strings.Join(lines, "\n")
	if after == before {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mimeapps.list.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(after + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Through a symlink (dotfile managers), replace the file it points to.
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	return os.Rename(tmp.Name(), path)
}

// getAssociation returns mimeType's IDs under section in an in-memory
// mimeapps.list.
func getAssociation(lines []string, section, mimeType string) []string {
	in := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			in = t == "["+section+"]"
			continue
		}
		if key, val, ok := strings.Cut(t, "="); in && ok && strings.TrimSpace(key) == mimeType {
			return splitList(val)
		}
	}
	return nil
}

// setAssociation sets mimeType's IDs under section in an in-memory
// mimeapps.list — replacing its line, or adding it (and the section) —
// or, with no IDs, removes it. Every other line stays as it was.
func setAssociation(lines []string, section, mimeType string, ids []string) []string {
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	entry := ""
	if len(ids) > 0 {
		entry = mimeType + "=" + strings.Join(ids, ";") + ";"
	}
	out := make([]string, 0, len(lines)+2)
	in, found, sectionEnd := false, false, -1
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if in {
				sectionEnd = len(out)
			}
			in = t == "["+section+"]"
			out = append(out, line)
			continue
		}
		if key, _, ok := strings.Cut(t, "="); in && ok && strings.TrimSpace(key) == mimeType {
			if entry != "" && !found {
				out = append(out, entry)
			}
			found = true
			continue
		}
		out = append(out, line)
	}
	if in {
		sectionEnd = len(out)
	}
	if found || entry == "" {
		return out
	}
	if sectionEnd < 0 {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		return append(out, "["+section+"]", entry)
	}
	// After the section's last entry, before any blank lines ending it.
	for sectionEnd > 0 && strings.TrimSpace(out[sectionEnd-1]) == "" {
		sectionEnd--
	}
	return append(out[:sectionEnd], append([]string{entry}, out[sectionEnd:]...)...)
}

// ParseMimeInput turns what a user typed to name a file type into a MIME
// type: either a MIME type itself ("image/png") or an extension, with or
// without its dot, or a file name ("png", ".png", "photo.png").
func ParseMimeInput(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("type a MIME type or an extension")
	}
	if strings.Contains(s, "/") {
		major, minor, _ := strings.Cut(s, "/")
		if major == "" || minor == "" || strings.ContainsAny(s, " =;[]") {
			return "", fmt.Errorf("%q is not a valid MIME type", s)
		}
		return loadMimeDB().unalias(strings.ToLower(s)), nil
	}
	name := s
	if !strings.Contains(name, ".") {
		name = "." + name
	}
	t := MimeType("file" + name)
	if t == "application/octet-stream" {
		return "", fmt.Errorf("unknown extension %q: type its MIME type instead", name)
	}
	return t, nil
}
