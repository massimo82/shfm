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

package vfs

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// LocalFS exposes the local machine's filesystem through the common VFS
// interface.
type LocalFS struct {
	label string
	root  string
}

// NewLocalFS creates a backend for the local filesystem rooted at root.
func NewLocalFS(label, root string) *LocalFS {
	if root == "" {
		root = "/"
	}
	return &LocalFS{label: label, root: root}
}

func (l *LocalFS) Kind() Kind    { return KindLocal }
func (l *LocalFS) Label() string { return l.label }
func (l *LocalFS) Root() string  { return l.root }

func (l *LocalFS) List(path string) ([]Entry, error) {
	des, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(des))
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			// Unreadable entry (broken link, permissions...): include it anyway.
			entries = append(entries, Entry{Name: de.Name(), IsDir: de.IsDir()})
			continue
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		isDir := de.IsDir()
		if isSymlink {
			// Follow the link to see whether it points at a directory.
			if target, err := os.Stat(path + "/" + de.Name()); err == nil {
				isDir = target.IsDir()
			}
		}
		owner, group := ownerGroup(info)
		entries = append(entries, Entry{
			Name:      de.Name(),
			IsDir:     isDir,
			IsSymlink: isSymlink,
			Size:      info.Size(),
			Mode:      info.Mode(),
			ModTime:   info.ModTime(),
			Owner:     owner,
			Group:     group,
		})
	}
	return entries, nil
}

func (l *LocalFS) Stat(path string) (Entry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	isSymlink := info.Mode()&os.ModeSymlink != 0
	isDir := info.IsDir()
	if isSymlink {
		if target, err := os.Stat(path); err == nil {
			isDir = target.IsDir()
		}
	}
	owner, group := ownerGroup(info)
	return Entry{
		Name:         filepath.Base(path),
		IsDir:        isDir,
		IsSymlink:    isSymlink,
		Size:         info.Size(),
		Mode:         info.Mode(),
		ModTime:      info.ModTime(),
		CreationTime: birthTime(path, false),
		Owner:        owner,
		Group:        group,
	}, nil
}

// BirthTime implements vfs.BirthTimer: a single, cheap statx(2) call
// (never a directory walk), meant to be called on-demand for just the one
// entry currently highlighted in the UI.
func (l *LocalFS) BirthTime(path string) (time.Time, error) {
	return birthTime(path, false), nil
}

func (l *LocalFS) Mkdir(path string) error { return os.Mkdir(path, 0o755) }
func (l *LocalFS) CreateEmptyFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// Remove predicts, from the parent directory's own owner/group/other
// permission bits (see removeOrRenameNeedsElevation), whether shfm can
// remove path at all — going straight to a pkexec-elevated `rm -rf`
// (Linux only) when it plausibly can't, rather than burning a doomed plain
// attempt first (worse than just slower for a recursive delete: it would
// remove everything it *could* before hitting the one entry it can't,
// leaving a partial deletion behind to then redo elevated). When the
// prediction says the plain attempt should work but it fails anyway
// (permissions changed in a race, an ACL exists — anything past this
// prediction's plain-Unix-bits view), that reactive path still runs too:
// see elevateIfPermissionError.
func (l *LocalFS) Remove(path string) error {
	// An immutable/append-only entry (or directory) can't be removed by
	// anyone, root included: say so now instead of asking for a password
	// that can't help (see attrs.go).
	if err := CheckRemovable(path); err != nil {
		return err
	}
	if removeOrRenameNeedsElevation(path) {
		return runElevated("rm", "-rf", "--", path)
	}
	err := os.RemoveAll(path)
	// The same, for something nested inside a folder being deleted, which
	// the check above couldn't see.
	if aerr := removeAttrError(err); aerr != nil {
		return aerr
	}
	return elevateIfPermissionError(err, func() error {
		return runElevated("rm", "-rf", "--", path)
	})
}

// Rename predicts, from source and destination parent directory
// permissions (see renameNeedsElevation), whether shfm can rename/move
// path at all — going straight to a pkexec-elevated `mv` (Linux only) when
// it plausibly can't, same reasoning and reactive fallback as Remove
// above. This covers both an in-place rename and a same-filesystem move
// (fileops.Move tries Rename first for exactly that reason).
func (l *LocalFS) Rename(oldPath, newPath string) error {
	if err := checkRenamable(oldPath, newPath); err != nil {
		return err
	}
	if renameNeedsElevation(oldPath, newPath) {
		return runElevated("mv", "--", oldPath, newPath)
	}
	err := os.Rename(oldPath, newPath)
	return elevateIfPermissionError(err, func() error {
		return runElevated("mv", "--", oldPath, newPath)
	})
}

func (l *LocalFS) Open(path string) (io.ReadCloser, error) { return os.Open(path) }
func (l *LocalFS) Create(path string) (io.WriteCloser, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
}
func (l *LocalFS) Join(elem ...string) string { return filepath.Join(elem...) }
func (l *LocalFS) Dir(path string) string     { return filepath.Dir(path) }
func (l *LocalFS) Base(path string) string    { return filepath.Base(path) }
func (l *LocalFS) SupportsTrash() bool        { return true }
func (l *LocalFS) Close() error               { return nil }

// DirSize implements DirSizer: it walks the subtree summing regular file
// sizes and counting every file and subfolder found (path itself
// excluded). It is best-effort — permission errors on individual entries
// are skipped rather than aborting the whole computation.
// errPseudoFS signals that path lives on a virtual/kernel filesystem
// (procfs, sysfs, ...) whose reported file sizes are not trustworthy —
// most notoriously /proc/kcore, which reports a fixed, fictitious 128TiB
// size representing the kernel's virtual address space, not real bytes on
// disk. DirSize refuses to walk such a tree at all rather than silently
// producing an absurd number; the UI leaves the size as "unknown" (it
// already does this for any DirSize error).
var errPseudoFS = errors.New("vfs: refusing to compute a recursive size on a virtual/kernel filesystem")

func (l *LocalFS) DirSize(path string) (size int64, itemCount int64, err error) {
	if isOnPseudoFS(path) {
		return 0, 0, errPseudoFS
	}
	walkErr := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries, keep walking
		}
		if p == path {
			return nil // don't count/size the root folder itself
		}
		itemCount++
		if !d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size, itemCount, walkErr
}

// Chmod implements PermissionsEditor. Only a file's owner (or root) may
// ever change its mode — chownNeedsElevation predicts exactly that (see
// its own doc comment) and goes straight to a pkexec-elevated `chmod`
// (Linux only) when the caller isn't the owner, rather than burning a
// plain attempt guaranteed to fail. When the prediction says it should
// work but it fails anyway, the existing reactive fallback still runs
// too: see elevateIfPermissionError.
func (l *LocalFS) Chmod(path string, mode os.FileMode) error {
	if err := checkChangeable(path); err != nil {
		return err
	}
	if chmodNeedsElevation(path) {
		return runElevated("chmod", fmt.Sprintf("%o", mode.Perm()), "--", path)
	}
	err := os.Chmod(path, mode)
	return elevateIfPermissionError(err, func() error {
		return runElevated("chmod", fmt.Sprintf("%o", mode.Perm()), "--", path)
	})
}

// Chown implements PermissionsEditor. chownNeedsElevation predicts
// whether this specific uid/gid change needs root — always true for a uid
// change, but a gid-only change to a group the caller already belongs to
// is allowed without it — and goes straight to a pkexec-elevated `chown`
// (Linux only) when so, rather than burning a plain attempt guaranteed to
// fail. When the prediction says it should work but it fails anyway, the
// existing reactive fallback still runs too: see elevateIfPermissionError.
func (l *LocalFS) Chown(path string, uid, gid int) error {
	if err := checkChangeable(path); err != nil {
		return err
	}
	if chownNeedsElevation(path, uid, gid) {
		return runElevated("chown", fmt.Sprintf("%d:%d", uid, gid), "--", path)
	}
	err := os.Chown(path, uid, gid)
	return elevateIfPermissionError(err, func() error {
		return runElevated("chown", fmt.Sprintf("%d:%d", uid, gid), "--", path)
	})
}

// Attributes implements AttrReader: the restricting chattr flags currently
// set on path ("immutable", "append-only"), nil if none or if they can't be
// read on this platform/filesystem.
func (l *LocalFS) Attributes(path string) []string {
	a, ok := readAttrs(path)
	if !ok {
		return nil
	}
	return a.names()
}

// LocalPath implements vfs.LocalPath: for the local backend, the VFS path
// already *is* the real filesystem path.
func (l *LocalFS) LocalPath(path string) (string, bool) { return path, true }

// ownerGroup best-effort extracts owner/group names from a fs.FileInfo's
// platform-specific Sys() data (available on Unix via syscall.Stat_t),
// resolving numeric UID/GID to human-readable names by parsing /etc/passwd
// and /etc/group directly — deliberately not using os/user, which (when
// cgo is available, the common case) shells out to libc via cgo; this
// project stays pure Go / cgo-free throughout. Falls back to the numeric ID
// as a string when the name can't be resolved, and to empty strings when
// Sys() isn't Stat_t-shaped (e.g. unsupported platform).
func ownerGroup(info fs.FileInfo) (owner, group string) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", ""
	}
	return userName(uint32(st.Uid)), groupName(uint32(st.Gid))
}

var (
	passwdMu    sync.Mutex
	passwdCache map[uint32]string

	groupMu    sync.Mutex
	groupCache map[uint32]string
)

func userName(uid uint32) string {
	passwdMu.Lock()
	defer passwdMu.Unlock()
	if passwdCache == nil {
		passwdCache = parseIDNameFile("/etc/passwd")
	}
	if name, ok := passwdCache[uid]; ok {
		return name
	}
	return strconv.FormatUint(uint64(uid), 10)
}

func groupName(gid uint32) string {
	groupMu.Lock()
	defer groupMu.Unlock()
	if groupCache == nil {
		groupCache = parseIDNameFile("/etc/group")
	}
	if name, ok := groupCache[gid]; ok {
		return name
	}
	return strconv.FormatUint(uint64(gid), 10)
}

// parseIDNameFile parses the common colon-separated format shared by
// /etc/passwd ("name:x:uid:gid:gecos:home:shell") and /etc/group
// ("name:x:gid:members"), both of which have the numeric ID as the third
// field, into an id -> name map.
func parseIDNameFile(path string) map[uint32]string {
	out := map[uint32]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			continue
		}
		id, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			continue
		}
		out[uint32(id)] = fields[0]
	}
	return out
}

// ResolveUser resolves a username or a numeric UID string to a numeric
// UID, by parsing /etc/passwd (see ownerGroup for why not os/user).
func ResolveUser(name string) (int, error) {
	if id, err := strconv.ParseUint(name, 10, 32); err == nil {
		return int(id), nil
	}
	passwdMu.Lock()
	if passwdCache == nil {
		passwdCache = parseIDNameFile("/etc/passwd")
	}
	cache := passwdCache
	passwdMu.Unlock()
	for id, n := range cache {
		if n == name {
			return int(id), nil
		}
	}
	return 0, errUnknownUser
}

// ResolveGroup resolves a group name or a numeric GID string to a numeric
// GID, by parsing /etc/group.
func ResolveGroup(name string) (int, error) {
	if id, err := strconv.ParseUint(name, 10, 32); err == nil {
		return int(id), nil
	}
	groupMu.Lock()
	if groupCache == nil {
		groupCache = parseIDNameFile("/etc/group")
	}
	cache := groupCache
	groupMu.Unlock()
	for id, n := range cache {
		if n == name {
			return int(id), nil
		}
	}
	return 0, errUnknownGroup
}

var (
	errUnknownUser  = fmtError("unknown user")
	errUnknownGroup = fmtError("unknown group")
)

type fmtError string

func (e fmtError) Error() string { return string(e) }
