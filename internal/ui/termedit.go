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
	"os"
	"os/exec"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/fusemount"
	"shfm/internal/opener"
	"shfm/internal/toolpath"
)

// Without a graphical session (a console, SSH without X forwarding) there's
// no window for a desktop application to open in: a text file, a
// configuration file included, is opened in a terminal editor instead,
// taking over shfm's own terminal until it exits.

// getenv and findEditor are variables so tests can fake the session and
// the installed editors.
var (
	getenv     = os.Getenv
	findEditor = toolpath.Find
)

// terminalEditors are tried in order: nano first, the friendliest to
// someone who just wants to change a line, then vim and plain vi.
var terminalEditors = []string{"nano", "vim", "vi"}

// graphicalSession tells whether a desktop application can open a window:
// a Wayland or X11 display is set.
func graphicalSession() bool {
	return getenv("WAYLAND_DISPLAY") != "" || getenv("DISPLAY") != ""
}

// isTextType tells whether a file of type mimeType is text a terminal
// editor can open: text/* and every type declared a text/plain subclass
// (shell scripts, JSON, YAML, TOML, XML, .desktop files...), and empty
// files, as configuration files often are.
func isTextType(mimeType string) bool {
	return mimeType == "application/x-zerosize" || opener.MimeTypeIs(mimeType, "text/plain")
}

// terminalEditor returns the path of the first installed terminalEditors.
func terminalEditor() (string, bool) {
	for _, name := range terminalEditors {
		if p, err := findEditor(name); err == nil {
			return p, true
		}
	}
	return "", false
}

// editorTarget is a file to open in the terminal editor: path is local,
// the file itself or, for a remote source that can't be mounted, a temp
// copy to upload back once changed (remote non-nil).
type editorTarget struct {
	editor string
	name   string
	path   string
	remote *remoteOpenTarget
	before os.FileInfo // the temp copy as downloaded
}

// editorReadyMsg carries a remote file made local, or why it couldn't be.
type editorReadyMsg struct {
	target editorTarget
	err    error
}

// editorDoneMsg reports the editor's exit, and a temp copy's upload.
type editorDoneMsg struct {
	target    editorTarget
	err       error
	uploaded  bool
	uploadErr error
}

// openInTerminalEditor opens a text file in a terminal editor when there's
// no graphical session, reporting whether it did: local files right away,
// remote ones once mounted, or copied locally, in the background.
func (m *Model) openInTerminalEditor(name, mimeType, realPath string, remote *remoteOpenTarget) bool {
	if graphicalSession() || !isTextType(mimeType) {
		return false
	}
	editor, ok := terminalEditor()
	if !ok {
		return false
	}
	target := editorTarget{editor: editor, name: name, path: realPath}
	if remote == nil {
		m.queueCmd(m.runEditor(target))
		return true
	}
	m.setStatus("Opening %s in %s…", name, filepath.Base(editor))
	mounts := m.mounts
	m.queueCmd(func() tea.Msg {
		if mounts != nil && fusemount.Supported(remote.fs) {
			if local, err := mounts.LocalPath(remote.fs, remote.path); err == nil {
				target.path = local
				return editorReadyMsg{target: target}
			}
		}
		temp, err := downloadToTemp(remote.fs, remote.path, remote.name)
		if err != nil {
			return editorReadyMsg{target: target, err: err}
		}
		target.path, target.remote = temp, remote
		target.before, _ = os.Stat(temp)
		return editorReadyMsg{target: target}
	})
	return true
}

// runEditor runs the editor on target in shfm's terminal, the TUI
// suspended until it exits; a changed temp copy is then uploaded back.
func (m *Model) runEditor(target editorTarget) tea.Cmd {
	cmd := exec.Command(target.editor, target.path)
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return editorFinished(target, err) })
}

// editorFinished uploads a changed temp copy back to its source, and
// removes it, once the editor exited with err.
func editorFinished(target editorTarget, err error) editorDoneMsg {
	done := editorDoneMsg{target: target, err: err}
	if target.remote != nil {
		if fileChanged(target.before, target.path) {
			done.uploadErr = uploadFromTemp(target.remote.fs, target.remote.path, target.path)
			done.uploaded = done.uploadErr == nil
		}
		os.RemoveAll(filepath.Dir(target.path))
	}
	return done
}

func (m *Model) handleEditorReady(msg editorReadyMsg) tea.Cmd {
	if msg.err != nil {
		m.setError("Could not open %s: %v", msg.target.name, msg.err)
		return nil
	}
	return m.runEditor(msg.target)
}

func (m *Model) handleEditorDone(msg editorDoneMsg) {
	m.activePane().Load()
	editor := filepath.Base(msg.target.editor)
	switch {
	case msg.uploadErr != nil:
		// Not losing the edit comes before how the editor exited
		m.setError("%s: changes not saved back to the source: %v", msg.target.name, msg.uploadErr)
	case msg.uploaded:
		m.setStatus("Edited %s in %s, saved back to the source", msg.target.name, editor)
	case msg.err != nil:
		m.setError("%s exited with an error: %v", editor, msg.err)
	default:
		m.setStatus("Edited %s in %s", msg.target.name, editor)
	}
}
