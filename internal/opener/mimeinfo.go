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
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// The freedesktop.org Shared MIME-info database (update-mime-database's
// output in each $XDG_DATA_DIRS/mime, the one GNOME, KDE and every
// freedesktop desktop read): file name globs with their weights, type
// aliases, the subclass hierarchy, and the "magic" rules recognizing a
// file by its content (magic.go) when its name doesn't tell.

type mimeGlob struct {
	weight  int
	mime    string
	pattern string
	cs      bool // case-sensitive (the "cs" flag)
}

type mimeDB struct {
	literals []mimeGlob // no wildcard: the whole file name
	suffixes []mimeGlob // "*" then no wildcard: an extension
	globs    []mimeGlob // anything else, matched as a shell glob
	aliases  map[string]string
	parents  map[string][]string
	exts     map[string][]string // type → ".ext", best glob first
	magic    []magicSection      // highest priority first
	extent   int                 // bytes of a file the magic rules look at
}

var (
	mimeDBMu    sync.Mutex
	mimeDBCache *mimeDB
	mimeDBKey   string
)

// dataDirs are the XDG base data directories, highest priority first.
func dataDirs() []string {
	var dirs []string
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		dirs = append(dirs, dataHome)
	} else if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "share"))
	}
	if env := os.Getenv("XDG_DATA_DIRS"); env != "" {
		for _, d := range strings.Split(env, ":") {
			if d != "" {
				dirs = append(dirs, d)
			}
		}
	} else {
		dirs = append(dirs, "/usr/local/share", "/usr/share")
	}
	return dirs
}

// loadMimeDB returns the database for the current data directories, read
// once and again only when they change.
func loadMimeDB() *mimeDB {
	var dirs []string
	for _, d := range dataDirs() {
		dirs = append(dirs, filepath.Join(d, "mime"))
	}
	key := strings.Join(dirs, "\x00")
	mimeDBMu.Lock()
	defer mimeDBMu.Unlock()
	if mimeDBCache != nil && mimeDBKey == key {
		return mimeDBCache
	}
	mimeDBCache, mimeDBKey = readMimeDB(dirs), key
	return mimeDBCache
}

func readMimeDB(dirs []string) *mimeDB {
	db := &mimeDB{aliases: map[string]string{}, parents: map[string][]string{}, exts: map[string][]string{}}
	// A type's __NOGLOBS__ in a directory drops its globs from the
	// directories below it; the same glob met again lower is a duplicate.
	blocked := map[string]bool{}
	seen := map[mimeGlob]bool{}
	for _, dir := range dirs {
		globs, noGlobs := readGlobs(dir)
		for _, g := range globs {
			if blocked[g.mime] || seen[g] {
				continue
			}
			seen[g] = true
			switch {
			case !strings.ContainsAny(g.pattern, "*?["):
				db.literals = append(db.literals, g)
			case strings.HasPrefix(g.pattern, "*") && !strings.ContainsAny(g.pattern[1:], "*?["):
				db.suffixes = append(db.suffixes, g)
			default:
				db.globs = append(db.globs, g)
			}
		}
		for t := range noGlobs {
			blocked[t] = true
		}
		db.addMagic(readMagic(filepath.Join(dir, "magic")))
		for _, pair := range readPairs(filepath.Join(dir, "aliases")) {
			if _, ok := db.aliases[pair[0]]; !ok {
				db.aliases[pair[0]] = pair[1]
			}
		}
		for _, pair := range readPairs(filepath.Join(dir, "subclasses")) {
			if !containsString(db.parents[pair[0]], pair[1]) {
				db.parents[pair[0]] = append(db.parents[pair[0]], pair[1])
			}
		}
	}
	sort.SliceStable(db.suffixes, func(i, j int) bool { return db.suffixes[i].weight > db.suffixes[j].weight })
	for _, g := range db.suffixes {
		ext := g.pattern[1:]
		if !strings.HasPrefix(ext, ".") || len(ext) < 2 {
			continue
		}
		dup := false
		for _, e := range db.exts[g.mime] {
			dup = dup || strings.EqualFold(e, ext)
		}
		if !dup {
			db.exts[g.mime] = append(db.exts[g.mime], ext)
		}
	}
	return db
}

// readGlobs reads a directory's globs2 ("weight:type:pattern[:flags]"),
// or the older globs ("type:pattern", weight 50) when there's no globs2.
func readGlobs(dir string) ([]mimeGlob, map[string]bool) {
	noGlobs := map[string]bool{}
	var out []mimeGlob
	lines, err := readLines(filepath.Join(dir, "globs2"))
	v2 := err == nil
	if !v2 {
		lines, _ = readLines(filepath.Join(dir, "globs"))
	}
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		g := mimeGlob{weight: 50}
		if v2 {
			fields := strings.SplitN(line, ":", 4)
			if len(fields) < 3 {
				continue
			}
			w, err := strconv.Atoi(fields[0])
			if err != nil {
				continue
			}
			g.weight, g.mime, g.pattern = w, fields[1], fields[2]
			if len(fields) == 4 {
				for _, f := range strings.Split(fields[3], ",") {
					g.cs = g.cs || f == "cs"
				}
			}
		} else {
			t, p, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			g.mime, g.pattern = t, p
		}
		if g.pattern == "__NOGLOBS__" {
			noGlobs[g.mime] = true
			continue
		}
		if g.mime != "" && g.pattern != "" {
			out = append(out, g)
		}
	}
	return out, noGlobs
}

// readPairs reads a "first second" per line file (aliases, subclasses).
func readPairs(path string) [][2]string {
	lines, _ := readLines(path)
	var out [][2]string
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) == 2 && !strings.HasPrefix(f[0], "#") {
			out = append(out, [2]string{f[0], f[1]})
		}
	}
	return out
}

func (db *mimeDB) empty() bool {
	return len(db.literals)+len(db.suffixes)+len(db.globs) == 0
}

// lookup returns the type of a file named name, by the spec's order: a
// literal name first, then the extensions, then the other globs; within
// each, the highest weight, then the longest pattern, then a
// case-sensitive one (main.C is C++, main.c C).
func (db *mimeDB) lookup(name string) (string, bool) {
	base := filepath.Base(name)
	lower := strings.ToLower(base)
	subject := func(g mimeGlob) (string, string) {
		if g.cs {
			return base, g.pattern
		}
		return lower, strings.ToLower(g.pattern)
	}
	stages := []struct {
		globs []mimeGlob
		match func(s, p string) bool
	}{
		{db.literals, func(s, p string) bool { return s == p }},
		{db.suffixes, func(s, p string) bool { return strings.HasSuffix(s, p[1:]) }},
		{db.globs, func(s, p string) bool { ok, _ := filepath.Match(p, s); return ok }},
	}
	for _, st := range stages {
		var best *mimeGlob
		for i := range st.globs {
			g := &st.globs[i]
			if s, p := subject(*g); !st.match(s, p) {
				continue
			}
			if best == nil || g.weight > best.weight ||
				(g.weight == best.weight && len(g.pattern) > len(best.pattern)) ||
				(g.weight == best.weight && len(g.pattern) == len(best.pattern) && g.cs && !best.cs) {
				best = g
			}
		}
		if best != nil {
			return db.unalias(best.mime), true
		}
	}
	return "", false
}

func (db *mimeDB) unalias(t string) string {
	if c, ok := db.aliases[t]; ok {
		return c
	}
	return t
}

// ancestors returns t's supertypes, nearest first: those the database
// declares, and text/plain for every text/* type. application/octet-stream,
// every file's implicit ancestor, is left out: an application for "any
// file" is no default for a particular one.
func (db *mimeDB) ancestors(t string) []string {
	var out []string
	queue := []string{db.unalias(t)}
	seen := map[string]bool{queue[0]: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		next := append([]string(nil), db.parents[cur]...)
		if strings.HasPrefix(cur, "text/") && cur != "text/plain" {
			next = append(next, "text/plain")
		}
		for _, p := range next {
			p = db.unalias(p)
			if !seen[p] && p != "application/octet-stream" {
				seen[p] = true
				out = append(out, p)
				queue = append(queue, p)
			}
		}
	}
	return out
}

// MimeType returns the type of a file named name, from the Shared
// MIME-info database's globs (Go's own table when the system has no
// database); application/octet-stream when nothing matches — see
// DetectMimeType to then look at the content.
func MimeType(name string) string {
	if t, ok := MimeTypeByName(name); ok {
		return t
	}
	return "application/octet-stream"
}

// MimeTypeByName is MimeType, false when the name matches nothing.
func MimeTypeByName(name string) (string, bool) {
	db := loadMimeDB()
	if !db.empty() {
		return db.lookup(name)
	}
	if ext := filepath.Ext(name); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			if i := strings.IndexByte(t, ';'); i >= 0 {
				t = t[:i]
			}
			return t, true
		}
	}
	return "", false
}

// MimeTypeIs reports whether a file of type t is of type pattern — the
// same type (or an alias of it), one of its supertypes (text/x-csrc is
// text/plain), or a match of a "major/*" pattern, by any of them — as
// GIO's g_content_type_is_a tells it.
func MimeTypeIs(t, pattern string) bool {
	db := loadMimeDB()
	pattern = db.unalias(strings.ToLower(pattern))
	for _, c := range append([]string{db.unalias(t)}, db.ancestors(t)...) {
		if ok, _ := path.Match(pattern, c); ok {
			return true
		}
	}
	return false
}

// Extensions returns the extensions of mimeType's files (".jpg", ...),
// the most specific first.
func Extensions(mimeType string) []string {
	db := loadMimeDB()
	if db.empty() {
		exts, _ := mime.ExtensionsByType(mimeType)
		return exts
	}
	return db.exts[db.unalias(mimeType)]
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
