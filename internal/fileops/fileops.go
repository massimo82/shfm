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

// Package fileops implements copy, move and delete, working both within the
// same VFS backend and across different backends (e.g. from local disk to
// an SMB share), through generic Open/Create-based streaming when no more
// efficient native operation is available. Every operation accepts an
// optional Progress reporter, used by the UI to drive a progress dialog and
// support cancellation for long-running background tasks.
package fileops

import (
	"fmt"
	"io"
	"os"
	"strings"

	"shfm/internal/trash"
	"shfm/internal/vfs"
)

// Item identifies a source file/folder within a FileSystem.
type Item struct {
	FS   vfs.FileSystem
	Path string // absolute path within FS
}

// Result summarizes the outcome of an operation over a set of items.
type Result struct {
	Done      int
	Errors    []error
	Cancelled bool
}

func (r *Result) Error() string {
	if len(r.Errors) == 0 {
		return ""
	}
	parts := make([]string, len(r.Errors))
	for i, e := range r.Errors {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "; ")
}

// Progress reports per-item progress and lets the caller cancel a
// long-running Copy/Move/Delete between items. Both fields are optional:
// a nil Progress, or nil fields within it, are simply not called.
type Progress struct {
	// OnItem is called once per top-level item, right after it finishes
	// (successfully or not): done is how many items have been processed so
	// far (including this one), total is len(items), name is the item's
	// display name, err is nil on success.
	OnItem func(done, total int, name string, err error)
	// Cancelled is polled before starting each item; when it returns true,
	// processing stops and Result.Cancelled is set.
	Cancelled func() bool
}

func (p *Progress) report(done, total int, name string, err error) {
	if p != nil && p.OnItem != nil {
		p.OnItem(done, total, name, err)
	}
}

func (p *Progress) cancelled() bool {
	return p != nil && p.Cancelled != nil && p.Cancelled()
}

// Copy copies every item into destDir (a destination folder on destFS). On
// a name collision in the SAME source/destination folder, the copy is
// renamed by appending " (copy)"; in other cases the destination is
// overwritten.
func Copy(items []Item, destFS vfs.FileSystem, destDir string, prog *Progress) *Result {
	res := &Result{}
	total := len(items)
	for _, it := range items {
		if prog.cancelled() {
			res.Cancelled = true
			break
		}
		name := it.FS.Base(it.Path)
		destPath := destFS.Join(destDir, name)
		if it.FS == destFS && it.FS.Dir(it.Path) == destDir {
			destPath = uniqueName(destFS, destDir, name, " (copy)")
		} else if destExists(destFS, destPath) {
			destPath = uniqueName(destFS, destDir, name, " (copy)")
		}
		err := copyRecursive(it.FS, it.Path, destFS, destPath)
		if err != nil {
			err = fmt.Errorf("copying %q: %w", it.Path, err)
			res.Errors = append(res.Errors, err)
		} else {
			res.Done++
		}
		prog.report(res.Done+len(res.Errors), total, name, err)
	}
	return res
}

// Move moves every item into destDir. If source and destination are on the
// same backend, it first tries a native Rename (instantaneous); otherwise
// (or if the backend doesn't support it) it falls back to copy followed by
// permanently deleting the source.
func Move(items []Item, destFS vfs.FileSystem, destDir string, prog *Progress) *Result {
	res := &Result{}
	total := len(items)
	for _, it := range items {
		if prog.cancelled() {
			res.Cancelled = true
			break
		}
		name := it.FS.Base(it.Path)
		err := moveOne(it, destFS, destDir, name)
		if err != nil {
			err = fmt.Errorf("moving %q: %w", it.Path, err)
			res.Errors = append(res.Errors, err)
		} else {
			res.Done++
		}
		prog.report(res.Done+len(res.Errors), total, name, err)
	}
	return res
}

func moveOne(it Item, destFS vfs.FileSystem, destDir, name string) error {
	destPath := destFS.Join(destDir, name)
	if it.FS == destFS {
		if destPath == it.Path {
			return fmt.Errorf("source and destination are the same")
		}
		if destExists(destFS, destPath) {
			destPath = uniqueName(destFS, destDir, name, " (moved)")
		}
		if err := it.FS.Rename(it.Path, destPath); err == nil {
			return nil
		} else if err != vfs.ErrNotSupported {
			return err
		}
		// ErrNotSupported: fall through to copy+delete below.
	}
	if destExists(destFS, destPath) {
		destPath = uniqueName(destFS, destDir, name, " (moved)")
	}
	if err := copyRecursive(it.FS, it.Path, destFS, destPath); err != nil {
		return err
	}
	return it.FS.Remove(it.Path)
}

// Delete deletes every item. When useTrash is true and the backend supports
// a native trash (only the local filesystem, per the Freedesktop Trash
// Specification), items are trashed instead of permanently removed — unless
// trashing fails for lack of permission (a root-owned item), in which case
// it falls back to a permanent delete instead of trying to make trashing it
// work too: the trash spec is inherently per-user (the trash can's own
// ownership/metadata), so there's no sensible "trash it as root" here, and
// FS.Remove has its own pkexec-elevated fallback anyway (see
// internal/vfs/elevate_linux.go).
func Delete(items []Item, useTrash bool, prog *Progress) *Result {
	res := &Result{}
	total := len(items)
	for _, it := range items {
		if prog.cancelled() {
			res.Cancelled = true
			break
		}
		name := it.FS.Base(it.Path)
		var err error
		if useTrash && it.FS.SupportsTrash() {
			// An immutable/append-only item can't be removed by anyone:
			// refuse now, rather than let MoveToTrash copy it (possibly a
			// huge file) into the trash before failing to remove the
			// original.
			if err = vfs.CheckRemovable(it.Path); err == nil {
				err = trash.MoveToTrash(it.Path)
				if err != nil && os.IsPermission(err) {
					err = it.FS.Remove(it.Path)
				}
			}
		} else {
			err = it.FS.Remove(it.Path)
		}
		if err != nil {
			err = fmt.Errorf("deleting %q: %w", it.Path, err)
			res.Errors = append(res.Errors, err)
		} else {
			res.Done++
		}
		prog.report(res.Done+len(res.Errors), total, name, err)
	}
	return res
}

// Rename renames a single item within the same backend.
func Rename(fs vfs.FileSystem, oldPath, newName string) error {
	newPath := fs.Join(fs.Dir(oldPath), newName)
	if destExists(fs, newPath) {
		return fmt.Errorf("an item named %q already exists", newName)
	}
	if err := fs.Rename(oldPath, newPath); err != nil {
		if err != vfs.ErrNotSupported {
			return err
		}
		if err := copyRecursive(fs, oldPath, fs, newPath); err != nil {
			return err
		}
		return fs.Remove(oldPath)
	}
	return nil
}

func destExists(fs vfs.FileSystem, path string) bool {
	_, err := fs.Stat(path)
	return err == nil
}

// uniqueName finds a free name in destDir by appending suffix (and a
// counter if needed) before the extension.
func uniqueName(fs vfs.FileSystem, destDir, name, suffix string) string {
	ext := ""
	base := name
	if idx := strings.LastIndex(name, "."); idx > 0 {
		base, ext = name[:idx], name[idx:]
	}
	candidate := fs.Join(destDir, base+suffix+ext)
	n := 2
	for destExists(fs, candidate) {
		candidate = fs.Join(destDir, fmt.Sprintf("%s%s %d%s", base, suffix, n, ext))
		n++
	}
	return candidate
}

// openDest opens a writer on the destination, using CreateSized when the
// backend requires it (currently only MTP, which must declare the object's
// size before the data phase) and the source size is known; otherwise falls
// back to plain Create.
func openDest(destFS vfs.FileSystem, destPath string, size int64) (io.WriteCloser, error) {
	if sc, ok := destFS.(vfs.SizedCreator); ok {
		return sc.CreateSized(destPath, size)
	}
	return destFS.Create(destPath)
}

// CopyTo copies srcPath (a file, or a folder recursively) to exactly
// destPath, overwriting what's there — no " (copy)" renaming, unlike Copy.
func CopyTo(srcFS vfs.FileSystem, srcPath string, destFS vfs.FileSystem, destPath string) error {
	return copyRecursive(srcFS, srcPath, destFS, destPath)
}

// copyRecursive copies a file or folder (recursively) from a source
// FileSystem to a destination FileSystem, even across different backends
// (local, SMB, NFS, MTP, SFTP), via Open/Create streaming.
func copyRecursive(srcFS vfs.FileSystem, srcPath string, destFS vfs.FileSystem, destPath string) error {
	entry, err := srcFS.Stat(srcPath)
	if err != nil {
		return err
	}
	if entry.IsDir {
		if err := destFS.Mkdir(destPath); err != nil && !destExists(destFS, destPath) {
			return err
		}
		children, err := srcFS.List(srcPath)
		if err != nil {
			return err
		}
		for _, c := range children {
			if err := copyRecursive(srcFS, srcFS.Join(srcPath, c.Name), destFS, destFS.Join(destPath, c.Name)); err != nil {
				return err
			}
		}
		return nil
	}

	r, err := srcFS.Open(srcPath)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := openDest(destFS, destPath, entry.Size)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
