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

package fileops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"shfm/internal/archive"
	"shfm/internal/vfs"
)

// errCancelled stops an extraction the user cancelled.
var errCancelled = errors.New("cancelled")

// Extract extracts every archive in items into a new folder next to it,
// named after the archive minus its extension ("photos.tar.gz" →
// "photos"; "photos (2)" and so on if that name is taken), on the
// archive's own backend. Entries that can't be extracted (unsafe names,
// encrypted or special files, symlinks where the backend has none) are
// skipped and reported as that archive's error, without stopping the
// rest. A cancelled extraction removes its partial folder.
func Extract(items []Item, prog *Progress) *Result {
	res := &Result{}
	total := len(items)
	for i, it := range items {
		if prog.cancelled() {
			res.Cancelled = true
			break
		}
		name := it.FS.Base(it.Path)
		err := extractOne(it, func(entry string) { prog.report(i, total, name+" › "+entry, nil) }, prog.cancelled)
		if errors.Is(err, errCancelled) {
			res.Cancelled = true
			break
		}
		if err != nil {
			err = fmt.Errorf("extracting %q: %w", it.Path, err)
			res.Errors = append(res.Errors, err)
		} else {
			res.Done++
		}
		prog.report(res.Done+len(res.Errors), total, name, err)
	}
	return res
}

// extractor writes one archive's entries into root, a folder it created.
type extractor struct {
	fs      vfs.FileSystem
	root    string
	local   bool       // fs paths are real local paths
	dirs    []dirAttrs // applied last, once nothing more is written inside
	links   []archive.Entry
	made    map[string]bool // folders known to exist, by entry name
	skipped []string
	wrote   bool
}

type dirAttrs struct {
	path    string
	mode    os.FileMode
	modTime time.Time
}

func extractOne(it Item, onEntry func(string), cancelled func() bool) error {
	kind, base := archive.Detect(it.FS.Base(it.Path))
	if kind == "" {
		return fmt.Errorf("not a recognised archive")
	}
	if base == "." || base == ".." {
		base = it.FS.Base(it.Path)
	}
	parent := it.FS.Dir(it.Path)
	root := uniqueDirName(it.FS, parent, base)
	if err := it.FS.Mkdir(root); err != nil {
		return err
	}

	x := &extractor{fs: it.FS, root: root, made: map[string]bool{".": true}}
	src := archive.Source{Name: it.Path, Open: func() (io.ReadCloser, error) { return it.FS.Open(it.Path) }}
	if lp, ok := it.FS.(vfs.LocalPath); ok {
		if p, ok := lp.LocalPath(it.Path); ok {
			src.LocalPath = p
			// A 7z/RAR extracted without bsdtar is staged next to its
			// destination: the system temp folder may be a small tmpfs.
			src.TempDir = filepath.Dir(p)
		}
		if _, ok := lp.LocalPath(root); ok {
			x.local = true
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer watchCancel(cancelled, cancel)()

	lastReport := time.Time{}
	err := archive.Walk(ctx, src, func(e archive.Entry, r io.Reader) error {
		if cancelled() {
			return errCancelled
		}
		if now := time.Now(); now.Sub(lastReport) > 150*time.Millisecond {
			lastReport = now
			onEntry(e.Name)
		}
		return x.entry(e, r)
	})
	if err == nil {
		err = x.finish()
	}
	if err != nil && (cancelled() || errors.Is(err, errCancelled) || errors.Is(err, context.Canceled)) {
		_ = it.FS.Remove(root)
		return errCancelled
	}
	if err != nil {
		if !x.wrote {
			_ = it.FS.Remove(root)
		}
		return err
	}
	if len(x.skipped) > 0 {
		return skippedError(x.skipped)
	}
	return nil
}

// skippedError summarizes the entries an extraction left out.
func skippedError(skipped []string) error {
	const shown = 3
	list := skipped
	if len(list) > shown {
		list = list[:shown]
	}
	msg := strings.Join(list, "; ")
	if len(skipped) > shown {
		msg += fmt.Sprintf("; and %d more", len(skipped)-shown)
	}
	return fmt.Errorf("%d entries skipped: %s", len(skipped), msg)
}

func (x *extractor) skip(err error) { x.skipped = append(x.skipped, err.Error()) }

// path returns the backend path of the entry named name.
func (x *extractor) path(name string) string {
	return x.fs.Join(append([]string{x.root}, strings.Split(name, "/")...)...)
}

// mkdirAll creates the folder named name and its missing parents.
func (x *extractor) mkdirAll(name string) error {
	if x.made[name] {
		return nil
	}
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		if err := x.mkdirAll(name[:i]); err != nil {
			return err
		}
	} else if err := x.mkdirAll("."); err != nil {
		return err
	}
	p := x.path(name)
	if err := x.fs.Mkdir(p); err != nil {
		if st, serr := x.fs.Stat(p); serr != nil || !st.IsDir {
			return err
		}
	}
	x.made[name] = true
	return nil
}

func parentName(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return "."
}

func (x *extractor) entry(e archive.Entry, r io.Reader) error {
	if e.Skip != nil {
		x.skip(e.Skip)
		return nil
	}
	switch e.Type {
	case archive.TypeDir:
		if err := x.mkdirAll(e.Name); err != nil {
			return err
		}
		x.wrote = true
		x.dirs = append(x.dirs, dirAttrs{x.path(e.Name), e.Mode, e.ModTime})
		return nil
	case archive.TypeSymlink:
		if !x.local {
			x.skip(fmt.Errorf("%s: symbolic links aren't supported on this destination", e.Name))
			return nil
		}
		// Created last, so no later entry can be written through one.
		x.links = append(x.links, e)
		return nil
	}

	if err := x.mkdirAll(parentName(e.Name)); err != nil {
		return err
	}
	dest := x.path(e.Name)
	if st, err := x.fs.Stat(dest); err == nil && st.IsDir {
		x.skip(fmt.Errorf("%s: a folder with that name was already extracted", e.Name))
		return nil
	}
	size := e.Size
	if e.Type == archive.TypeHardlink {
		// Hard links become copies: most backends have none.
		target := x.path(e.Linkname)
		st, err := x.fs.Stat(target)
		if err != nil || st.IsDir {
			x.skip(fmt.Errorf("%s: link to %s, which wasn't extracted", e.Name, e.Linkname))
			return nil
		}
		rc, err := x.fs.Open(target)
		if err != nil {
			return err
		}
		defer rc.Close()
		r, size = rc, st.Size
	}
	if err := x.writeFile(dest, r, size); err != nil {
		return fmt.Errorf("%s: %w", e.Name, err)
	}
	x.wrote = true
	x.setAttrs(dest, e.Mode, e.ModTime)
	return nil
}

// writeFile writes r to dest. A backend that must know a file's size
// before writing it (MTP) gets content of unknown size spooled to a
// temporary file first.
func (x *extractor) writeFile(dest string, r io.Reader, size int64) error {
	if _, sized := x.fs.(vfs.SizedCreator); sized && size < 0 {
		tmp, err := os.CreateTemp("", "shfm-extract-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if size, err = io.Copy(tmp, r); err != nil {
			return err
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		r = tmp
	}
	w, err := openDest(x.fs, dest, size)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// setAttrs applies an entry's permissions (local destinations only, and
// never setuid/setgid/sticky) and modification time, where the backend
// can: best-effort, a failure here isn't worth failing the entry for.
func (x *extractor) setAttrs(p string, mode os.FileMode, modTime time.Time) {
	if x.local && mode != 0 {
		_ = os.Chmod(p, mode.Perm())
	}
	if ts, ok := x.fs.(vfs.TimesSetter); ok && !modTime.IsZero() {
		_ = ts.Chtimes(p, time.Time{}, modTime)
	}
}

// finish creates the symlinks, then applies the folders' attributes,
// deepest first so setting a child's time doesn't bump its parent's.
func (x *extractor) finish() error {
	for _, l := range x.links {
		if !archive.SafeSymlink(l.Name, l.Linkname) {
			x.skip(fmt.Errorf("%s: symbolic link to %s, outside the archive's folder", l.Name, l.Linkname))
			continue
		}
		if err := x.mkdirAll(parentName(l.Name)); err != nil {
			return err
		}
		// SafeSymlink's guarantee needs real folders above the link.
		if x.throughSymlink(l.Name) {
			x.skip(fmt.Errorf("%s: inside a symbolic link", l.Name))
			continue
		}
		p := x.path(l.Name)
		if err := os.Symlink(l.Linkname, p); err != nil {
			x.skip(fmt.Errorf("%s: %w", l.Name, err))
			continue
		}
		x.wrote = true
	}
	sort.SliceStable(x.dirs, func(i, j int) bool {
		return strings.Count(x.dirs[i].path, "/") > strings.Count(x.dirs[j].path, "/")
	})
	for _, d := range x.dirs {
		mode := d.mode
		if mode != 0 {
			// Keep the folder usable by its new owner.
			mode |= 0o700
		}
		x.setAttrs(d.path, mode, d.modTime)
	}
	return nil
}

// throughSymlink reports whether any folder above the entry named name is
// a symlink (local destinations only).
func (x *extractor) throughSymlink(name string) bool {
	for dir := parentName(name); dir != "."; dir = parentName(dir) {
		if st, err := os.Lstat(x.path(dir)); err == nil && st.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// uniqueDirName returns parent/base, or parent/"base (N)" for the first N
// from 2 that isn't taken.
func uniqueDirName(fs vfs.FileSystem, parent, base string) string {
	candidate := fs.Join(parent, base)
	for n := 2; destExists(fs, candidate); n++ {
		candidate = fs.Join(parent, fmt.Sprintf("%s (%d)", base, n))
	}
	return candidate
}
