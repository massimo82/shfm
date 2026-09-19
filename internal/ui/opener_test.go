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
	"path/filepath"
	"testing"

	"shfm/internal/vfs"
)

func TestOpenWithDefaultAppShowsChooserWhenNoAssociation(t *testing.T) {
	dataHome := t.TempDir()
	configHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	// Also sandbox the *_DIRS (system-wide) lookups: left unset, opener
	// falls back to the real /usr/share/applications, /usr/local/share/
	// applications and /etc/xdg, leaking whatever's actually installed on
	// the machine running the test (e.g. a real system default for
	// text/plain would make the "no association" premise of this test
	// false, and it'd fail for a reason that has nothing to do with the
	// code under test).
	t.Setenv("XDG_DATA_DIRS", t.TempDir())
	t.Setenv("XDG_CONFIG_DIRS", t.TempDir())

	// Register one fake, launchable application so ListApps() finds it.
	appsDir := filepath.Join(dataHome, "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	desktop := "[Desktop Entry]\nName=Fake Editor\nExec=true %f\n"
	if err := os.WriteFile(filepath.Join(appsDir, "fake.desktop"), []byte(desktop), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTestModel()
	m.panes[m.active] = NewPane(vfs.NewLocalFS("Local", dir), dir, false, m.active, m.sizeCh)
	m.panes[m.active].Load()

	var entry vfs.Entry
	for _, e := range m.panes[m.active].Entries {
		if e.Name == "note.txt" {
			entry = e
		}
	}
	if entry.Name == "" {
		t.Fatal("note.txt not found in pane listing")
	}

	m.openWithDefaultApp(entry)

	if m.dialog.Kind != DialogChooseApp {
		t.Fatalf("expected DialogChooseApp when no default association exists, got %v (status=%q)", m.dialog.Kind, m.status)
	}
	if len(m.dialog.Items) == 0 {
		t.Fatal("expected at least the fake app in the chooser")
	}
	found := false
	for _, it := range m.dialog.Items {
		if it == "Fake Editor" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'Fake Editor' among choices, got %v", m.dialog.Items)
	}
}
