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

package ui

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/applog"
	"shfm/internal/fusemount"
	"shfm/internal/opener"
	"shfm/internal/vfs"
)

// remoteOpenTarget identifies an entry on a source with no real local path
// (SMB/NFS/SFTP/MTP): opening it with an external app requires either the
// source's FUSE mount (see internal/fusemount) or a local temp copy.
type remoteOpenTarget struct {
	fs   vfs.FileSystem
	path string // vfs path of the entry within fs
	name string // entry name, for the temp file's basename and status messages
}

// openResultMsg carries the progress/outcome of opening a remote entry
// with an app. Through the source's FUSE mount, a single message reports
// the launch. Through a temp copy, two are sent: an interim one
// (editing=true) once the app has launched on the downloaded copy, and a
// final one once it's exited (with the sync outcome, if anything changed).
type openResultMsg struct {
	requestID   int
	name        string
	app         opener.App
	mounted     bool  // opened in place through the source's FUSE mount
	downloading error // interim: the mount failed with this, downloading a temp copy instead
	editing     bool  // interim: app launched, waiting for it to exit
	changed     bool  // final: the temp copy was modified before the app exited
	err         error
}

func (m *Model) waitForOpenMsg() tea.Cmd {
	return func() tea.Msg {
		return <-m.openCh
	}
}

// startOpenRemote launches app on target, entirely in the background: even
// a slow network source, or a long editing session, must never freeze the
// UI.
//
// A remote source (SMB/NFS/SFTP/MTP) is opened in place, through its FUSE
// mount (mounted on first use): the app reads and writes the remote file
// directly, so a video player starts streaming at once and saves go
// straight to the source — the same as opening a file from gvfs's mount in
// other file managers. Where the mount isn't possible (FUSE unavailable),
// it falls back to a temp copy: see openViaTempCopy.
func (m *Model) startOpenRemote(target *remoteOpenTarget, app opener.App) {
	id := m.nextOpenID
	m.nextOpenID++
	ch := m.openCh
	if m.mounts == nil || !fusemount.Supported(target.fs) {
		m.setStatus("Downloading %s to open with %s…", target.name, app.Name)
		go openViaTempCopy(ch, id, target, app)
		return
	}
	mounts := m.mounts
	m.setStatus("Opening %s with %s…", target.name, app.Name)
	go func() {
		local, err := mounts.LocalPath(target.fs, target.path)
		if err != nil {
			applog.Warn("could not mount source, opening a temp copy instead",
				"source", target.fs.Label(), "error", err)
			ch <- openResultMsg{requestID: id, name: target.name, app: app, downloading: err}
			openViaTempCopy(ch, id, target, app)
			return
		}
		err = opener.Launch(app, local)
		ch <- openResultMsg{requestID: id, name: target.name, app: app, mounted: true, err: err}
	}()
}

// exposeSource mounts a newly opened network source in the background, so
// that other applications' file dialogs list it for as long as shfm runs
// (see fusemount.Manager.Expose). Nothing in shfm depends on it: if the
// mount fails, it's only logged, and opening a file with an app mounts
// the source then (or falls back to a temp copy) as usual.
func (m *Model) exposeSource(fs vfs.FileSystem) {
	if m.mounts == nil {
		return
	}
	mounts := m.mounts
	go func() {
		if err := mounts.Expose(fs); err != nil && !errors.Is(err, vfs.ErrNotSupported) {
			applog.Warn("could not mount source for other applications",
				"source", fs.Label(), "error", err)
		}
	}()
}

// openViaTempCopy downloads target to a local temp file, launches app on
// it, and — once the app exits — re-uploads the temp file to target if it
// was modified. Runs in its own goroutine, reporting on ch.
//
// "The app exited" is a best-effort proxy for "the user is done editing":
// an app that hands off to an already-running instance (common for
// single-instance GUI apps, e.g. most browsers/editors with a running
// background process) may exit immediately, well before the user actually
// closes the document in that other instance — in which case the edit
// won't be synced back until shfm is asked to open the same file again
// (or the user copies it back manually). This is an inherent limit of a
// temp-copy round trip, which is why network sources are opened through
// their FUSE mount instead whenever possible.
func openViaTempCopy(ch chan<- openResultMsg, id int, target *remoteOpenTarget, app opener.App) {
	tempPath, err := downloadToTemp(target.fs, target.path, target.name)
	if err != nil {
		ch <- openResultMsg{requestID: id, name: target.name, app: app, err: err}
		return
	}
	ch <- openResultMsg{requestID: id, name: target.name, app: app, editing: true}

	before, _ := os.Stat(tempPath)
	waitErr := opener.LaunchAndWait(app, tempPath)
	changed := fileChanged(before, tempPath)

	var finalErr error
	if changed {
		// Prioritize not losing the edit over reporting how the app
		// exited: some apps report a nonzero exit status on an
		// otherwise perfectly normal quit.
		finalErr = uploadFromTemp(target.fs, target.path, tempPath)
	} else {
		finalErr = waitErr
	}
	os.RemoveAll(filepath.Dir(tempPath))
	ch <- openResultMsg{requestID: id, name: target.name, app: app, changed: changed, err: finalErr}
}

// fileChanged reports whether the file at path differs from its state
// before (as captured by an earlier os.Stat), by mtime or size — the same
// cheap heuristic tools like rsync use for "did this change", well short of
// a full content hash.
func fileChanged(before os.FileInfo, path string) bool {
	after, err := os.Stat(path)
	if err != nil {
		return false
	}
	if before == nil {
		return true
	}
	return after.ModTime().After(before.ModTime()) || after.Size() != before.Size()
}

// downloadToTemp copies the entry at vfsPath (within fs) into a freshly
// created temp directory, under its original name so extension-based
// MIME/app detection by the target application still works.
func downloadToTemp(fs vfs.FileSystem, vfsPath, name string) (string, error) {
	dir, err := os.MkdirTemp("", "shfm-open-*")
	if err != nil {
		return "", err
	}
	dest := filepath.Join(dir, filepath.Base(name))

	src, err := fs.Open(vfsPath)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	defer src.Close()

	out, err := os.Create(dest)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	if _, err := io.Copy(out, src); err != nil {
		out.Close()
		os.RemoveAll(dir)
		return "", err
	}
	if err := out.Close(); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dest, nil
}

// uploadFromTemp copies the local temp file back to vfsPath within fs,
// overwriting the remote original.
func uploadFromTemp(fs vfs.FileSystem, vfsPath, tempPath string) error {
	src, err := os.Open(tempPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := fs.Create(vfsPath)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return err
	}
	return dst.Close()
}

func (m *Model) handleOpenResult(msg openResultMsg) {
	if msg.err != nil {
		m.setError("Could not open %s: %v", msg.name, msg.err)
		return
	}
	switch {
	case msg.downloading != nil:
		m.setStatus("Could not mount the source (%v): downloading %s to open with %s…", msg.downloading, msg.name, msg.app.Name)
	case msg.mounted:
		m.setStatus("Opened %s with %s", msg.name, msg.app.Name)
	case msg.editing:
		m.setStatus("Editing %s with %s…", msg.name, msg.app.Name)
	case msg.changed:
		m.setStatus("%s edited with %s — saved back", msg.name, msg.app.Name)
		// The size/mtime shown for the entry may now be stale.
		m.panes[0].Load()
		m.panes[1].Load()
	default:
		m.setStatus("Opened %s with %s", msg.name, msg.app.Name)
	}
}
