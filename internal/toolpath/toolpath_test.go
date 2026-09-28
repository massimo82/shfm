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

package toolpath

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeMachine makes PATH hold only pathDir, and the known folders dirs —
// without touching the real PATH.
func fakeMachine(t *testing.T, pathDir string, dirs ...string) {
	t.Helper()
	oldLook, oldDirs := lookPath, searchDirs
	t.Cleanup(func() { lookPath, searchDirs = oldLook, oldDirs })
	lookPath = func(name string) (string, error) {
		if strings.Contains(name, "/") {
			return exec.LookPath(name)
		}
		if p, err := exec.LookPath(filepath.Join(pathDir, name)); err == nil {
			return p, nil
		}
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	searchDirs = func() []string { return dirs }
}

func writeProgram(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestFind(t *testing.T) {
	path, first, second := t.TempDir(), t.TempDir(), t.TempDir()
	fakeMachine(t, path, first, second)
	writeProgram(t, filepath.Join(path, "both"), 0o755)
	writeProgram(t, filepath.Join(second, "both"), 0o755)
	writeProgram(t, filepath.Join(first, "plain"), 0o644) // not executable
	writeProgram(t, filepath.Join(second, "plain"), 0o755)
	os.Mkdir(filepath.Join(first, "dir"), 0o755)

	for _, c := range []struct{ name, want string }{
		{"both", filepath.Join(path, "both")},     // PATH first
		{"plain", filepath.Join(second, "plain")}, // skipping what can't run
		{filepath.Join(second, "both"), filepath.Join(second, "both")},
	} {
		if got, err := Find(c.name); err != nil || got != c.want {
			t.Errorf("Find(%q) = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	for _, name := range []string{"dir", "missing", filepath.Join(first, "plain")} {
		if got, err := Find(name); err == nil {
			t.Errorf("Find(%q) = %q, want an error", name, got)
		} else if name == "missing" && !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("Find(missing): %v, want PATH's not found", err)
		}
	}
}

func TestFindFile(t *testing.T) {
	dir := t.TempDir()
	fakeMachine(t, dir)
	writeProgram(t, filepath.Join(dir, "off"), 0o644)
	writeProgram(t, filepath.Join(dir, "on"), 0o755)
	if got, want := FindFile(filepath.Join(dir, "none"), filepath.Join(dir, "off"), filepath.Join(dir, "on")), filepath.Join(dir, "on"); got != want {
		t.Errorf("FindFile = %q, want %q", got, want)
	}
	if got := FindFile(filepath.Join(dir, "off")); got != "" {
		t.Errorf("FindFile of a non-executable = %q", got)
	}
}

func TestKnownDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USER", "someone")
	dirs := knownDirs()
	for _, want := range []string{"/usr/bin", "/run/current-system/sw/bin", "/etc/profiles/per-user/someone/bin", filepath.Join(home, ".local", "bin")} {
		if !slices.Contains(dirs, want) {
			t.Errorf("%s not searched: %v", want, dirs)
		}
	}
	// The user's own folders come after every system one.
	if slices.Index(dirs, filepath.Join(home, ".local", "bin")) < slices.Index(dirs, "/usr/bin") {
		t.Errorf("user folders before system ones: %v", dirs)
	}
}
