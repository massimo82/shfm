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
	"io"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"shfm/internal/opener"
	"shfm/internal/vfs"
)

// remoteOpenTarget identifies an entry on a source with no real local path
// (SMB/NFS/SFTP/MTP): opening it with an external app requires downloading
// it to a local temp copy first.
type remoteOpenTarget struct {
	fs   vfs.FileSystem
	path string // vfs path of the entry within fs
	name string // entry name, for the temp file's basename and status messages
}

// openResultMsg carries the progress/outcome of downloading a remote entry
// to a local temp file, launching an app on it, and — once the app exits —
// syncing any change back to the source. Two messages are sent per request:
// an interim one (editing=true) once the app has launched, and a final one
// once it's exited (with the sync outcome, if anything changed).
type openResultMsg struct {
	requestID int
	name      string
	app       opener.App
	editing   bool // interim: app launched, waiting for it to exit
	changed   bool // final: the temp copy was modified before the app exited
	err       error
}

func (m *Model) waitForOpenMsg() tea.Cmd {
	return func() tea.Msg {
		return <-m.openCh
	}
}

// startOpenRemote downloads target to a local temp file, launches app on
// it, and — once the app exits — re-uploads the temp file to target if it
// was modified, entirely in the background: even a slow network source, or
// a long editing session, must never freeze the UI.
//
// "The app exited" is a best-effort proxy for "the user is done editing":
// an app that hands off to an already-running instance (common for
// single-instance GUI apps, e.g. most browsers/editors with a running
// background process) may exit immediately, well before the user actually
// closes the document in that other instance — in which case the edit
// won't be synced back until shfm is asked to open the same file again
// (or the user copies it back manually). This is an inherent limit of a
// temp-copy round trip without a live filesystem mount (FUSE), which shfm
// deliberately doesn't set up.
func (m *Model) startOpenRemote(target *remoteOpenTarget, app opener.App) {
	id := m.nextOpenID
	m.nextOpenID++
	ch := m.openCh
	m.setStatus("Downloading %s to open with %s…", target.name, app.Name)
	go func() {
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
	}()
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
