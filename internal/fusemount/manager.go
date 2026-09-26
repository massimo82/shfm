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

//go:build linux

// Package fusemount exposes network sources (SMB, NFS, SFTP) as local
// folders through FUSE — the same idea as gvfs's FUSE bridge, implemented
// independently in Go on top of shfm's own backends — so that external
// applications can open remote files in place: a video player streams and
// seeks through a film on an SMB share instead of waiting for shfm to
// download all of it, and an editor saves straight back to the server.
//
// The FUSE protocol itself is served by github.com/hanwen/go-fuse (pure
// Go). Only the mount(2) call goes through the system's setuid fusermount3
// helper, since an unprivileged process can't mount a FUSE filesystem by
// itself.
package fusemount

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"shfm/internal/vfs"
)

// Manager owns the FUSE mounts of one shfm process. Each network source is
// mounted once, on first use, under <base>/<pid>/<source name>, and stays
// mounted until UnmountAll — whatever the UI later does with its own
// connection to that source, since an application may still be using the
// file it opened. Safe for concurrent use.
type Manager struct {
	dir string // this process's folder of mount points

	mu     sync.Mutex
	mounts map[string]*mount // by sourceKey
}

type mount struct {
	dir    string
	server *fuse.Server
	mfs    *mountFS
	done   chan struct{} // closed once the server stops serving
}

// NewManager returns a Manager whose mount points live under base (normally
// DefaultBase()). It first cleans up after shfm processes that are no longer
// running, whose mounts would otherwise be left behind, stale, after a
// crash.
func NewManager(base string) *Manager {
	cleanupStale(base)
	return &Manager{
		dir:    filepath.Join(base, strconv.Itoa(os.Getpid())),
		mounts: map[string]*mount{},
	}
}

// DefaultBase returns the folder shfm keeps its mount points in:
// $XDG_RUNTIME_DIR/shfm (a per-user tmpfs, as gvfs uses), falling back to
// a per-user folder in the system temp dir.
func DefaultBase() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "shfm")
	}
	return filepath.Join(os.TempDir(), "shfm-"+strconv.Itoa(os.Getuid()))
}

// Supported reports whether src can be mounted: it must support random
// access, and either open a connection of its own for the mount or be
// shareable (vfs.SingleSession).
func Supported(src vfs.FileSystem) bool {
	_, redial := src.(vfs.Redialer)
	_, single := src.(vfs.SingleSession)
	_, ra := src.(vfs.RandomAccessOpener)
	return ra && (redial || single)
}

func isSingleSession(src vfs.FileSystem) bool {
	_, ok := src.(vfs.SingleSession)
	return ok
}

func sourceKey(src vfs.FileSystem) string {
	return src.Kind().String() + "|" + src.Label()
}

// LocalPath returns the local path of the entry at vfsPath of src, on the
// FUSE mount of src, mounting src first if it isn't mounted yet. Mounting
// opens a new connection to the source, so it blocks: never call it from
// the UI's event loop. Returns vfs.ErrNotSupported for a source that can't
// be mounted.
func (mg *Manager) LocalPath(src vfs.FileSystem, vfsPath string) (string, error) {
	if !Supported(src) {
		return "", vfs.ErrNotSupported
	}
	mnt, err := mg.ensure(src)
	if err != nil {
		return "", err
	}
	return filepath.Join(mnt.dir, filepath.FromSlash(vfsPath)), nil
}

func (mg *Manager) ensure(src vfs.FileSystem) (*mount, error) {
	key := sourceKey(src)
	shared := isSingleSession(src)
	mg.mu.Lock()
	if mnt, ok := mg.mounts[key]; ok && mnt.alive() && (!shared || mnt.mfs.be == src) {
		mg.mu.Unlock()
		return mnt, nil
	}
	mg.mu.Unlock()

	be := src
	if !shared {
		// Connect without holding the lock: it can take several seconds.
		var err error
		if be, err = src.(vfs.Redialer).Redial(); err != nil {
			return nil, err
		}
	}
	closeBe := func() {
		if !shared {
			be.Close()
		}
	}
	mfs, err := newMountFS(be, shared)
	if err != nil {
		closeBe()
		return nil, err
	}

	mg.mu.Lock()
	defer mg.mu.Unlock()
	if mnt, ok := mg.mounts[key]; ok && mnt.alive() {
		if !shared || mnt.mfs.be == src {
			// Mounted by a concurrent call in the meantime.
			closeBe()
			return mnt, nil
		}
		// A mount sharing an older session with the same device (the UI
		// has reconnected since): that session is no longer the one in use.
		mg.unmountLocked(key, mnt)
	}
	dir, err := mg.mountDirLocked(src)
	if err != nil {
		closeBe()
		return nil, err
	}
	mnt, err := mountAt(dir, src.Label(), mfs)
	if err != nil {
		closeBe()
		os.Remove(dir)
		return nil, err
	}
	mg.mounts[key] = mnt
	return mnt, nil
}

// Release is called by the UI when it stops using src (closes a pane on
// it). It returns true if src is a shared session a live mount still
// relies on: the mount then takes it over, closing it when it's
// unmounted, and the caller must not close it. See Reclaim.
func (mg *Manager) Release(src vfs.FileSystem) bool {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	for _, mnt := range mg.mounts {
		if mnt.alive() && mnt.mfs.shared && mnt.mfs.be == src {
			mnt.mfs.ownsBackend.Store(true)
			return true
		}
	}
	return false
}

// Session returns the live shared session a mount holds for the source
// labeled label of the given kind, if any — so that the UI, reopening
// a single-session source (an MTP device) whose session the mount has
// taken over, reuses it rather than failing to open a second one. The UI
// owns the returned session again, as before Release. A session that no
// longer responds (the device was unplugged) is dropped along with its
// mount, and Session returns nil.
func (mg *Manager) Session(kind vfs.Kind, label string) vfs.FileSystem {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	key := kind.String() + "|" + label
	mnt, ok := mg.mounts[key]
	if !ok || !mnt.alive() || !mnt.mfs.shared {
		return nil
	}
	be := mnt.mfs.be
	if sr, ok := be.(vfs.SpaceReporter); ok {
		if _, _, err := sr.Space("/"); err != nil {
			mg.unmountLocked(key, mnt)
			return nil
		}
	}
	mnt.mfs.ownsBackend.Store(false)
	return be
}

// mountDirLocked creates and returns a fresh, empty mount point for src,
// named after it.
func (mg *Manager) mountDirLocked(src vfs.FileSystem) (string, error) {
	if err := os.MkdirAll(mg.dir, 0o700); err != nil {
		return "", err
	}
	// Ensure the per-process folder is private even if it already existed
	// (or its parent was created with a looser umask).
	os.Chmod(filepath.Dir(mg.dir), 0o700)
	os.Chmod(mg.dir, 0o700)

	name := dirName(src.Label())
	for i := 1; ; i++ {
		candidate := name
		if i > 1 {
			candidate = fmt.Sprintf("%s-%d", name, i)
		}
		dir := filepath.Join(mg.dir, candidate)
		if err := os.Mkdir(dir, 0o700); err == nil {
			return dir, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		// Taken by a live mount (or the leftover of a dead one): leave it,
		// try the next name.
	}
}

// dirName turns a source label ("smb://nas/video") into a readable,
// filesystem-safe folder name ("smb-nas-video").
func dirName(label string) string {
	label = strings.Replace(label, "://", "-", 1)
	var b strings.Builder
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '@', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-.")
	if name == "" {
		name = "source"
	}
	return name
}

func mountAt(dir, label string, mfs *mountFS) (*mount, error) {
	timeout := time.Second
	opts := &fs.Options{
		EntryTimeout:    &timeout,
		AttrTimeout:     &timeout,
		NegativeTimeout: &timeout,
		UID:             mfs.uid,
		GID:             mfs.gid,
		MountOptions: fuse.MountOptions{
			FsName: label,
			Name:   "shfm",
			// Every xattr request would otherwise be a round trip for
			// nothing (the kernel asks for security.capability before
			// every write).
			DisableXAttrs: true,
		},
	}
	server, err := fs.Mount(dir, &node{m: mfs}, opts)
	if err != nil {
		return nil, fmt.Errorf("could not mount %s on %s: %w", label, dir, err)
	}
	mnt := &mount{dir: dir, server: server, mfs: mfs, done: make(chan struct{})}
	go func() {
		// Returns once unmounted, including by an external fusermount -u.
		server.Wait()
		mfs.close()
		close(mnt.done)
	}()
	return mnt, nil
}

func (mnt *mount) alive() bool {
	select {
	case <-mnt.done:
		return false
	default:
		return true
	}
}

// Busy reports whether an application still has files open on any mount.
func (mg *Manager) Busy() bool {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	for _, mnt := range mg.mounts {
		if mnt.alive() && mnt.mfs.openFiles.Load() > 0 {
			return true
		}
	}
	return false
}

// UnmountAll unmounts every mount and removes the mount points. An
// application still using a file on one loses access to it: a mount that
// is busy is detached lazily (fusermount3 -u -z), and the connection
// serving it closes with shfm.
func (mg *Manager) UnmountAll() {
	mg.mu.Lock()
	defer mg.mu.Unlock()
	for key, mnt := range mg.mounts {
		mg.unmountLocked(key, mnt)
	}
	os.Remove(mg.dir)
}

// unmountLocked unmounts mnt (lazily if it's busy) and forgets it; mg.mu
// must be held.
func (mg *Manager) unmountLocked(key string, mnt *mount) {
	if mnt.alive() {
		if err := mnt.server.Unmount(); err != nil {
			lazyUnmount(mnt.dir)
		}
	}
	mnt.mfs.close()
	os.Remove(mnt.dir)
	delete(mg.mounts, key)
}

// lazyUnmount detaches the mount at dir even if it's busy or its server is
// gone (a stale mount after a crash). Best effort.
func lazyUnmount(dir string) {
	for _, name := range []string{"fusermount3", "fusermount"} {
		if bin, err := exec.LookPath(name); err == nil {
			if exec.Command(bin, "-u", "-z", dir).Run() == nil {
				return
			}
		}
	}
}

// cleanupStale unmounts and removes the mount points left under base by
// shfm processes that are no longer running.
func cleanupStale(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() || pid == os.Getpid() || processRunning(pid) {
			continue
		}
		procDir := filepath.Join(base, e.Name())
		mounts, _ := os.ReadDir(procDir)
		for _, m := range mounts {
			dir := filepath.Join(procDir, m.Name())
			lazyUnmount(dir)
			os.Remove(dir)
		}
		os.Remove(procDir)
	}
}

// processRunning reports whether pid is a running shfm process (a pid
// reused by an unrelated process counts as not running).
func processRunning(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return true // can't tell: assume it is, never unmount a live one
	}
	return strings.HasPrefix(strings.TrimSpace(string(comm)), "shfm")
}
