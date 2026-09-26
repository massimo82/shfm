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

package mirror

import (
	"fmt"
	"sort"
	"strings"

	"shfm/internal/applog"
	"shfm/internal/fileops"
	"shfm/internal/vfs"
)

// tree is a recursive listing: path relative to the root ("a/b.txt") →
// entry. bad lists the relative folders ("" = the root) whose listing
// failed, so their contents are unknown.
type tree struct {
	entries map[string]vfs.Entry
	bad     []string
}

func walk(fs vfs.FileSystem, root string) tree {
	t := tree{entries: map[string]vfs.Entry{}}
	var rec func(rel string)
	rec = func(rel string) {
		children, err := fs.List(fs.Join(root, rel))
		if err != nil {
			applog.Warn("mirror: listing failed", "path", fs.Join(root, rel), "error", err)
			t.bad = append(t.bad, rel)
			return
		}
		for _, c := range children {
			r := c.Name
			if rel != "" {
				r = rel + "/" + c.Name
			}
			t.entries[r] = c
			if c.IsDir && !c.IsSymlink {
				rec(r)
			}
		}
	}
	rec("")
	return t
}

// unknown reports whether rel lies under a folder whose listing failed.
func (t tree) unknown(rel string) bool {
	for _, b := range t.bad {
		if b == "" || rel == b || strings.HasPrefix(rel, b+"/") {
			return true
		}
	}
	return false
}

// op is one planned change.
type op struct {
	kind string // "delete" | "mkdir" | "copy"
	rel  string
}

// runGeneric mirrors with plain listing + streaming copies, using the
// manifest at statePath to tell which files changed since last time.
func runGeneric(src, dst Side, statePath string, prog *fileops.Progress) *fileops.Result {
	res := &fileops.Result{}
	srcRoot, err := src.FS.Stat(src.Path)
	if err != nil {
		return fail(res, prog, fmt.Errorf("source: %w", err))
	}
	if srcRoot.IsSymlink {
		return fail(res, prog, fmt.Errorf("source %s is a symlink", src.Path))
	}
	old := loadManifest(statePath)
	next := manifest{}
	defer func() {
		if err := next.save(statePath); err != nil {
			applog.Warn("mirror: saving state failed", "path", statePath, "error", err)
		}
	}()

	if !srcRoot.IsDir {
		mirrorFile(src, dst, "", srcRoot, old, next, res, prog, 0, 1)
		return res
	}

	// Make sure the destination root is a folder.
	if d, err := dst.FS.Stat(dst.Path); err == nil && !d.IsDir {
		if err := dst.FS.Remove(dst.Path); err != nil {
			return fail(res, prog, err)
		}
		if err := dst.FS.Mkdir(dst.Path); err != nil {
			return fail(res, prog, fmt.Errorf("creating %s: %w", dst.Path, err))
		}
	} else if err != nil {
		if err := dst.FS.Mkdir(dst.Path); err != nil {
			return fail(res, prog, fmt.Errorf("creating %s: %w", dst.Path, err))
		}
	}

	st := walk(src.FS, src.Path)
	if st.unknown("") {
		return fail(res, prog, fmt.Errorf("source %s can't be listed", src.Path))
	}
	for rel, e := range st.entries {
		if e.IsSymlink {
			applog.Debug("mirror: skipping symlink", "path", src.FS.Join(src.Path, rel))
			delete(st.entries, rel)
		}
	}
	dt := walk(dst.FS, dst.Path)

	var ops []op
	// Deletions first: anything the source doesn't have (or has with a
	// different kind), except where the source listing failed. Sorted, so
	// a deleted folder's contents (which follow it) are skipped.
	var deleted []string
	for _, rel := range sortedKeys(dt.entries) {
		if under(rel, deleted) || st.unknown(rel) {
			continue
		}
		d := dt.entries[rel]
		s, ok := st.entries[rel]
		if !ok || s.IsDir != (d.IsDir && !d.IsSymlink) {
			ops = append(ops, op{"delete", rel})
			deleted = append(deleted, rel)
		}
	}
	for _, rel := range sortedKeys(st.entries) {
		s := st.entries[rel]
		d, have := dt.entries[rel]
		if have && (under(rel, deleted) || isDeleted(rel, deleted)) {
			have = false
		}
		switch {
		case s.IsDir:
			if !have {
				ops = append(ops, op{"mkdir", rel})
			}
		case !have || needsCopy(s, d, old, rel):
			ops = append(ops, op{"copy", rel})
		default:
			next[rel] = old[rel]
		}
	}

	total := len(ops)
	for i, o := range ops {
		if cancelled(prog) {
			res.Cancelled = true
			break
		}
		var err error
		switch o.kind {
		case "delete":
			err = dst.FS.Remove(dst.FS.Join(dst.Path, o.rel))
		case "mkdir":
			err = dst.FS.Mkdir(dst.FS.Join(dst.Path, o.rel))
		case "copy":
			err = copyFile(src, dst, o.rel, st.entries[o.rel], next)
		}
		if err != nil {
			err = fmt.Errorf("%s %s: %w", o.kind, o.rel, err)
			res.Errors = append(res.Errors, err)
		} else {
			res.Done++
		}
		report(prog, i+1, total, o.rel, err)
	}
	if total == 0 {
		report(prog, 0, 0, "up to date", nil)
	}
	return res
}

// mirrorFile handles a single-file mirror (rel "").
func mirrorFile(src, dst Side, rel string, s vfs.Entry, old, next manifest, res *fileops.Result, prog *fileops.Progress, done, total int) {
	d, err := dst.FS.Stat(dst.Path)
	have := err == nil
	if have && d.IsDir {
		if err := dst.FS.Remove(dst.Path); err != nil {
			res.Errors = append(res.Errors, err)
			return
		}
		have = false
	}
	if have && !needsCopy(s, d, old, rel) {
		next[rel] = old[rel]
		report(prog, 0, 0, "up to date", nil)
		return
	}
	if err := copyFile(src, dst, rel, s, next); err != nil {
		res.Errors = append(res.Errors, err)
		report(prog, total, total, dst.FS.Base(dst.Path), err)
		return
	}
	res.Done++
	report(prog, total, total, dst.FS.Base(dst.Path), nil)
}

// needsCopy reports whether the destination file d is stale: its size
// differs from the source's, or either side changed since the manifest
// recorded them.
func needsCopy(s, d vfs.Entry, old manifest, rel string) bool {
	if d.IsDir || s.Size != d.Size {
		return true
	}
	st, ok := old[rel]
	return !ok || st.SrcSize != s.Size || !st.SrcMod.Equal(s.ModTime) ||
		st.DstSize != d.Size || !st.DstMod.Equal(d.ModTime)
}

// copyFile copies one file and records the result in next.
func copyFile(src, dst Side, rel string, s vfs.Entry, next manifest) error {
	sp, dp := src.Path, dst.Path
	if rel != "" {
		sp, dp = src.FS.Join(src.Path, rel), dst.FS.Join(dst.Path, rel)
	}
	// MTP can't overwrite: creating an existing name adds a second object.
	if dst.FS.Kind() == vfs.KindMTP {
		if _, err := dst.FS.Stat(dp); err == nil {
			if err := dst.FS.Remove(dp); err != nil {
				return err
			}
		}
	}
	if err := fileops.CopyTo(src.FS, sp, dst.FS, dp); err != nil {
		return err
	}
	d, err := dst.FS.Stat(dp)
	if err != nil {
		return err
	}
	next[rel] = fileState{SrcSize: s.Size, SrcMod: s.ModTime, DstSize: d.Size, DstMod: d.ModTime}
	return nil
}

func sortedKeys(m map[string]vfs.Entry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func isDeleted(rel string, deleted []string) bool {
	i := sort.SearchStrings(deleted, rel)
	return i < len(deleted) && deleted[i] == rel
}

// under reports whether rel is inside one of the folders in dirs.
func under(rel string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}
