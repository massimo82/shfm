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

// Package vfs provides a filesystem abstraction (Virtual File System) that
// lets the file manager treat different sources uniformly: local disk,
// SMB/CIFS shares, NFS exports and MTP devices. Each backend implements the
// FileSystem interface.
package vfs

import (
	"io"
	"os"
	"time"
)

// Entry represents a file or folder entry inside a FileSystem.
type Entry struct {
	Name      string // name only, no path
	IsDir     bool
	IsSymlink bool
	Size      int64 // for directories, populated lazily by DirSizer when available
	Mode      os.FileMode
	ModTime   time.Time

	// CreationTime is the file's birth time, when the backend/filesystem
	// can determine it (on Linux, via the statx(2) syscall's STATX_BTIME —
	// not every filesystem records it, e.g. tmpfs doesn't); zero when
	// unknown or not applicable for this backend, in which case the UI
	// simply omits it rather than showing a misleading date.
	CreationTime time.Time

	// ItemCount is the total number of files and subfolders contained
	// (recursively) in a directory, populated lazily alongside Size by
	// DirSizer when available; meaningless (zero) for regular files.
	ItemCount int64

	// Owner/Group are best-effort human-readable owner/group names (empty
	// when unknown or not applicable for this backend).
	Owner string
	Group string
}

// Kind identifies the backend type.
type Kind int

const (
	KindLocal Kind = iota
	KindSMB
	KindNFS
	KindMTP
	KindSFTP
)

func (k Kind) String() string {
	switch k {
	case KindLocal:
		return "local"
	case KindSMB:
		return "smb"
	case KindNFS:
		return "nfs"
	case KindMTP:
		return "mtp"
	case KindSFTP:
		return "sftp"
	default:
		return "?"
	}
}

// FileSystem is the interface implemented by every backend (local, SMB,
// NFS, MTP). Paths passed to its methods are always "absolute" paths within
// the backend itself, separated by '/', rooted at "/".
type FileSystem interface {
	// Kind returns the backend type.
	Kind() Kind

	// Label is a human-readable label shown in the UI (e.g. host or mount point).
	Label() string

	// Root returns the initial root path to show when opened.
	Root() string

	// List lists the contents of a folder.
	List(path string) ([]Entry, error)

	// Stat returns information about a path.
	Stat(path string) (Entry, error)

	// Mkdir creates a new folder (non-recursive, like mkdir).
	Mkdir(path string) error

	// CreateEmptyFile creates a new, empty file.
	CreateEmptyFile(path string) error

	// Remove permanently deletes a file or folder (recursively for folders).
	Remove(path string) error

	// Rename renames/moves a file within the same backend, when natively
	// supported (more efficient than copy+delete). If unsupported, returns
	// ErrNotSupported and the caller should fall back to copy+delete.
	Rename(oldPath, newPath string) error

	// Open opens a file for reading.
	Open(path string) (io.ReadCloser, error)

	// Create opens (creating or truncating) a file for writing.
	Create(path string) (io.WriteCloser, error)

	// Join joins path elements according to the backend's conventions.
	Join(elem ...string) string

	// Dir returns the parent folder path.
	Dir(path string) string

	// Base returns the last element of the path.
	Base(path string) string

	// SupportsTrash reports whether the backend has a usable native trash
	// (only the local filesystem implements the Freedesktop Trash
	// Specification).
	SupportsTrash() bool

	// Close closes any network connection opened by the backend.
	Close() error
}

// SizedCreator is an optional interface a backend may implement when it
// needs to know the final object size before starting to write (as the MTP
// protocol requires, declaring the size in the ObjectInfo dataset before the
// data phase). fileops uses it automatically, when available, for copies
// where the source size is already known.
type SizedCreator interface {
	CreateSized(path string, size int64) (io.WriteCloser, error)
}

// DirSizer is an optional interface a backend may implement to compute the
// total recursive size, and item count, of a folder's contents. Only
// implemented by the local backend: over SMB/NFS/MTP/SFTP, walking a whole
// subtree just to display a size would be too slow.
type DirSizer interface {
	// DirSize returns the total size (in bytes) of every regular file
	// under path, and the total count of files and subfolders found while
	// walking it (path itself excluded).
	DirSize(path string) (size int64, itemCount int64, err error)
}

// PermissionsEditor is an optional interface a backend may implement to
// support viewing and changing POSIX permissions/ownership. Only meaningful
// (and only implemented) for the local filesystem.
type PermissionsEditor interface {
	Chmod(path string, mode os.FileMode) error
	Chown(path string, uid, gid int) error
}

// OwnerModeSetter is an optional interface a PermissionsEditor may also
// implement to change a file's owner/group and mode in one go — for the
// local backend, with at most one elevation (so one password prompt) when
// either change needs root.
type OwnerModeSetter interface {
	SetOwnerAndMode(path string, uid, gid int, mode os.FileMode) error
}

// AttrReader is an optional interface a backend may implement to report the
// attribute flags (Linux chattr/lsattr: immutable, append-only) that make an
// entry unchangeable even by root. Implemented by the local backend only.
type AttrReader interface {
	// Attributes returns the names of the restricting flags set on path,
	// nil if there are none or they can't be determined.
	Attributes(path string) []string
}

// BirthTimer is an optional interface a backend may implement to report a
// single file's creation ("birth") time cheaply (a single syscall, not a
// directory walk) — unlike DirSizer, meant to be called synchronously,
// on-demand, only for the one entry currently shown in the UI's per-pane
// detail line. Only implemented by the local backend: over SMB/NFS/MTP/
// SFTP, even a single extra round-trip per cursor movement would make
// browsing feel laggy.
type BirthTimer interface {
	BirthTime(path string) (time.Time, error)
}

// LocalPath is an optional interface a backend may implement when its
// entries correspond to real paths on the local filesystem, letting the UI
// open them with the desktop's default application. Only the local backend
// implements it (SMB/NFS/MTP content has no meaningful local path to open
// with an external application).
type LocalPath interface {
	LocalPath(path string) (string, bool)
}

// ErrNotSupported indicates the operation isn't natively supported by the backend.
var ErrNotSupported = errNotSupported{}

type errNotSupported struct{}

func (errNotSupported) Error() string { return "operation not supported by this backend" }
