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
	"time"

	"shfm/internal/config"
	"shfm/internal/vfs"
)

// mirrorTestModel returns a model whose config and mirror state live in
// temp dirs, with a synthetic tree at <tmp>/src/data and an empty
// <tmp>/dst, the active pane showing <tmp>/dst and data in the clipboard.
func mirrorTestModel(t *testing.T) (*Model, string) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	for p, content := range map[string]string{"src/data/a.txt": "alpha", "src/data/sub/b.txt": "beta"} {
		full := filepath.Join(tmp, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(content), 0o644)
	}
	os.MkdirAll(filepath.Join(tmp, "dst"), 0o755)

	m := New(config.Default(), config.DefaultKeyMap(), tmp)
	fs := vfs.NewLocalFS("Local", tmp)
	m.panes[0] = NewPane(fs, filepath.Join(tmp, "dst"), false, 0, nil)
	// Keep the other pane off the real home folder too.
	m.panes[1] = NewPane(fs, filepath.Join(tmp, "src"), false, 1, nil)
	m.active = 0
	m.clipboard = Clipboard{FS: fs, Dir: filepath.Join(tmp, "src"), Names: []string{"data"}}
	return m, tmp
}

// waitTasks feeds task updates to the model until every task finished.
func waitTasks(t *testing.T, m *Model) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for m.hasRunningTasks() {
		select {
		case msg := <-m.taskCh:
			m.handleTaskMsg(msg)
		case <-deadline:
			t.Fatal("tasks did not finish")
		}
	}
}

func TestMirrorPasteCreatesPairAndSyncs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		itemIdx  int
		useRsync bool
	}{{"rsync", 0, true}, {"generic", 1, false}} {
		t.Run(tc.name, func(t *testing.T) {
			m, tmp := mirrorTestModel(t)
			m.doMirrorPaste()
			if m.dialog.Kind != DialogMirrorConfirm {
				t.Fatalf("dialog = %v, want DialogMirrorConfirm", m.dialog.Kind)
			}
			if len(m.dialog.Items) != 2 {
				t.Fatalf("local → local should offer the backend choice, got %q", m.dialog.Items)
			}
			m.dialog.ItemIdx = tc.itemIdx
			m.confirmDialog()
			if len(m.cfg.MirrorPairs) != 1 || m.cfg.MirrorPairs[0].UseRsync != tc.useRsync {
				t.Fatalf("pairs = %+v", m.cfg.MirrorPairs)
			}
			waitTasks(t, m)
			got, err := os.ReadFile(filepath.Join(tmp, "dst", "data", "sub", "b.txt"))
			if err != nil || string(got) != "beta" {
				t.Fatalf("mirrored file: %q, %v", got, err)
			}
			// Saved to the (temp) config file.
			if cfg := config.Load(); len(cfg.MirrorPairs) != 1 {
				t.Fatalf("saved config has %d pairs", len(cfg.MirrorPairs))
			}
		})
	}
}

func TestMirrorPasteRejectsOverlap(t *testing.T) {
	m, tmp := mirrorTestModel(t)
	m.panes[0] = NewPane(m.clipboard.FS, filepath.Join(tmp, "src", "data"), false, 0, nil)
	m.doMirrorPaste()
	if m.dialog.Kind != DialogNone || !m.statusErr {
		t.Fatalf("mirroring a folder into itself should fail, dialog=%v status=%q", m.dialog.Kind, m.status)
	}
}

func TestMirrorEmptyClipboardOpensList(t *testing.T) {
	m, _ := mirrorTestModel(t)
	m.clipboard = Clipboard{}
	m.doMirrorPaste()
	if m.dialog.Kind != DialogMirrorList {
		t.Fatalf("dialog = %v, want DialogMirrorList", m.dialog.Kind)
	}
}

func TestMirrorScheduling(t *testing.T) {
	m, tmp := mirrorTestModel(t)
	m.doMirrorPaste()
	m.confirmDialog()
	waitTasks(t, m)
	id := m.cfg.MirrorPairs[0].ID

	// Just synced: not due yet.
	m.checkMirrors()
	if m.hasRunningTasks() {
		t.Fatal("mirror re-ran before the interval elapsed")
	}

	// Interval elapsed: runs again and picks up the change.
	os.WriteFile(filepath.Join(tmp, "src", "data", "new.txt"), []byte("new"), 0o644)
	m.mirrors[id].lastRun = time.Now().Add(-mirrorInterval - time.Second)
	m.checkMirrors()
	waitTasks(t, m)
	if _, err := os.Stat(filepath.Join(tmp, "dst", "data", "new.txt")); err != nil {
		t.Fatal("scheduled run did not sync the new file")
	}
	// Only the latest run of the pair stays in the task list.
	if len(m.tasks) != 1 {
		t.Fatalf("%d tasks listed, want 1", len(m.tasks))
	}

	// Paused pairs don't run.
	m.cfg.MirrorPairs[0].Paused = true
	m.mirrors[id].lastRun = time.Time{}
	m.checkMirrors()
	if m.hasRunningTasks() {
		t.Fatal("paused mirror ran")
	}
}

func TestMirrorDelete(t *testing.T) {
	m, _ := mirrorTestModel(t)
	m.doMirrorPaste()
	m.confirmDialog()
	waitTasks(t, m)
	id := m.cfg.MirrorPairs[0].ID
	m.deleteMirror(id)
	if len(m.cfg.MirrorPairs) != 0 {
		t.Fatal("pair not deleted")
	}
}

// closeCounter is a non-local source that counts Close calls.
type closeCounter struct {
	vfs.FileSystem
	closed int
}

func (c *closeCounter) Kind() vfs.Kind { return vfs.KindSMB }
func (c *closeCounter) Close() error   { c.closed++; return nil }

func TestLeasedSourceClosedAfterMirror(t *testing.T) {
	m, _ := mirrorTestModel(t)
	fs := &closeCounter{FileSystem: vfs.NewLocalFS("x", "/")}
	m.leaseFS(fs)
	m.closeFSWhenUnused(fs) // a pane switched away while a mirror uses it
	if fs.closed != 0 {
		t.Fatal("closed while still in use")
	}
	m.releaseFS(fs)
	if fs.closed != 1 {
		t.Fatalf("closed %d times after the mirror ended, want 1", fs.closed)
	}
}
