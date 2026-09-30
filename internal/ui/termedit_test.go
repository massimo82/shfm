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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shfm/internal/vfs"
)

// fakeSession makes the session graphical (a Wayland display set) or not,
// whatever the machine running the test has.
func fakeSession(t *testing.T, graphical bool) {
	t.Helper()
	old := getenv
	t.Cleanup(func() { getenv = old })
	getenv = func(key string) string {
		if graphical && key == "WAYLAND_DISPLAY" {
			return "wayland-1"
		}
		if key == "WAYLAND_DISPLAY" || key == "DISPLAY" {
			return ""
		}
		return os.Getenv(key)
	}
}

// fakeEditors makes only the named terminal editors installed, at
// /fake/NAME.
func fakeEditors(t *testing.T, installed ...string) {
	t.Helper()
	old := findEditor
	t.Cleanup(func() { findEditor = old })
	findEditor = func(name string) (string, error) {
		for _, n := range installed {
			if n == name {
				return "/fake/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
}

// TestGraphicalSession: a Wayland or an X11 display is a graphical
// session, neither is none.
func TestGraphicalSession(t *testing.T) {
	for _, tc := range []struct {
		wayland, x11 string
		want         bool
	}{
		{"wayland-1", "", true},
		{"", ":0", true},
		{"", "", false},
	} {
		old := getenv
		getenv = func(key string) string {
			return map[string]string{"WAYLAND_DISPLAY": tc.wayland, "DISPLAY": tc.x11}[key]
		}
		if got := graphicalSession(); got != tc.want {
			t.Errorf("WAYLAND_DISPLAY=%q DISPLAY=%q: graphicalSession() = %v, want %v", tc.wayland, tc.x11, got, tc.want)
		}
		getenv = old
	}
}

// TestTerminalEditorPrefersNano: nano when installed, then vim, then vi.
func TestTerminalEditorPrefersNano(t *testing.T) {
	for _, tc := range []struct {
		installed []string
		want      string
	}{
		{[]string{"vi", "vim", "nano"}, "/fake/nano"},
		{[]string{"vi", "vim"}, "/fake/vim"},
		{[]string{"vi"}, "/fake/vi"},
		{nil, ""},
	} {
		fakeEditors(t, tc.installed...)
		got, ok := terminalEditor()
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("installed %v: terminalEditor() = %q, %v; want %q", tc.installed, got, ok, tc.want)
		}
	}
}

// termEditEnv sets up a MIME database where shell scripts and JSON are
// text by their declared supertypes, and a folder with one file of each
// kind.
func termEditEnv(t *testing.T) string {
	t.Helper()
	sysData := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_DATA_DIRS", sysData)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_DIRS", t.TempDir())
	files := map[string]string{
		filepath.Join(sysData, "mime", "globs2"): "50:text/plain:*.txt\n50:application/x-shellscript:*.sh\n" +
			"50:application/json:*.json\n50:application/javascript:*.js\n50:application/pdf:*.pdf\n",
		filepath.Join(sysData, "mime", "subclasses"): "application/x-shellscript text/plain\n" +
			"application/json application/javascript\napplication/javascript text/plain\n",
		filepath.Join(sysData, "mime", "magic"): "MIME-Magic\x00\n[50:application/pdf]\n>0=\x00\x04%PDF\n",
		// An application for no type in particular: the chooser lists it
		filepath.Join(sysData, "applications", "editor.desktop"): "[Desktop Entry]\nType=Application\nName=Editor\nExec=true %f\n",
	}
	dir := t.TempDir()
	for name, content := range map[string]string{
		"notes.txt": "hello\n", "run.sh": "#!/bin/sh\n", "app.json": "{}\n",
		"report.pdf": "%PDF-1.7", "settings": "key = value\n", "empty.conf": "",
	} {
		files[filepath.Join(dir, name)] = content
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestIsTextType: text/plain and its subtypes, however deep, and empty
// files; not other types.
func TestIsTextType(t *testing.T) {
	termEditEnv(t)
	for mime, want := range map[string]bool{
		"text/plain":                true,
		"text/x-csrc":               true,
		"application/x-shellscript": true,
		"application/json":          true,
		"application/x-zerosize":    true,
		"application/pdf":           false,
		"image/png":                 false,
	} {
		if got := isTextType(mime); got != want {
			t.Errorf("isTextType(%q) = %v, want %v", mime, got, want)
		}
	}
}

// openEntry opens the named entry of the active pane as Enter would.
func openEntry(t *testing.T, m *Model, name string) {
	t.Helper()
	for _, e := range m.activePane().Entries {
		if e.Name == name {
			m.openWithDefaultApp(e)
			return
		}
	}
	t.Fatalf("no entry %s", name)
}

// TestOpenTextInTerminalEditor: with no graphical session, text files —
// by extension, by supertype, by content, empty — go to the terminal
// editor, run by Update; other files, or any file in a graphical session,
// or with no editor installed, go the usual way.
func TestOpenTextInTerminalEditor(t *testing.T) {
	dir := termEditEnv(t)
	newModel := func() *Model {
		m := newTestModel()
		m.panes[m.active] = NewPane(vfs.NewLocalFS("Local", dir), dir, false, m.active, m.sizeCh)
		return m
	}

	fakeSession(t, false)
	fakeEditors(t, "nano", "vim")
	for _, name := range []string{"notes.txt", "run.sh", "app.json", "settings", "empty.conf"} {
		m := newModel()
		openEntry(t, m, name)
		if len(m.queued) != 1 || m.dialog.Kind != DialogNone {
			t.Errorf("%s with no graphical session: %d commands queued, dialog %v; want the editor", name, len(m.queued), m.dialog.Kind)
		}
		if _, cmd := m.Update(nil); cmd == nil || len(m.queued) != 0 {
			t.Errorf("%s: Update didn't run the queued editor", name)
		}
	}

	m := newModel()
	openEntry(t, m, "report.pdf")
	if len(m.queued) != 0 || m.dialog.Kind != DialogChooseApp {
		t.Errorf("a PDF with no graphical session: %d commands queued, dialog %v; want the chooser", len(m.queued), m.dialog.Kind)
	}

	fakeEditors(t)
	m = newModel()
	openEntry(t, m, "notes.txt")
	if len(m.queued) != 0 || m.dialog.Kind != DialogChooseApp {
		t.Errorf("no editor installed: %d commands queued, dialog %v; want the chooser", len(m.queued), m.dialog.Kind)
	}

	fakeSession(t, true)
	fakeEditors(t, "nano")
	m = newModel()
	openEntry(t, m, "notes.txt")
	if len(m.queued) != 0 || m.dialog.Kind != DialogChooseApp {
		t.Errorf("graphical session: %d commands queued, dialog %v; want the chooser", len(m.queued), m.dialog.Kind)
	}
}

// TestTerminalEditorRemoteTempCopy: a remote file that can't be mounted is
// edited as a temp copy, uploaded back only when changed, and removed.
func TestTerminalEditorRemoteTempCopy(t *testing.T) {
	dir := termEditEnv(t)
	fakeSession(t, false)
	fakeEditors(t, "nano")
	m := newTestModel() // no mount manager: a temp copy
	fs := vfs.NewLocalFS("Remote", dir)
	source := filepath.Join(dir, "notes.txt")
	remote := &remoteOpenTarget{fs: fs, path: source, name: "notes.txt"}

	if !m.openInTerminalEditor("notes.txt", "text/plain", "", remote) || len(m.queued) != 1 {
		t.Fatal("the remote text file wasn't opened in the editor")
	}
	ready, ok := m.queued[0]().(editorReadyMsg)
	if !ok || ready.err != nil || ready.target.remote != remote || ready.target.editor != "/fake/nano" {
		t.Fatalf("ready = %+v", ready)
	}
	temp := ready.target.path
	if data, _ := os.ReadFile(temp); string(data) != "hello\n" {
		t.Fatalf("temp copy %s holds %q", temp, data)
	}

	// Unchanged: nothing uploaded, the copy removed
	done := editorFinished(ready.target, nil)
	if done.uploaded || done.uploadErr != nil {
		t.Errorf("unchanged copy: %+v", done)
	}
	if _, err := os.Stat(filepath.Dir(temp)); !os.IsNotExist(err) {
		t.Errorf("temp copy's folder left behind: %v", err)
	}

	// Changed: uploaded back
	m.openInTerminalEditor("notes.txt", "text/plain", "", remote)
	ready = m.queued[1]().(editorReadyMsg)
	if err := os.WriteFile(ready.target.path, []byte("hello, edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	done = editorFinished(ready.target, errors.New("exit status 1"))
	if !done.uploaded {
		t.Fatalf("changed copy not uploaded: %+v", done)
	}
	if data, _ := os.ReadFile(source); string(data) != "hello, edited\n" {
		t.Errorf("source holds %q after the upload", data)
	}
	m.handleEditorDone(done)
	if !strings.Contains(m.status, "saved back to the source") {
		t.Errorf("status %q, want the upload reported", m.status)
	}
}
