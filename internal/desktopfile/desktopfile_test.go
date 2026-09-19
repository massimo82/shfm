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

package desktopfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureInstalledCreatesUserFileWhenAbsent(t *testing.T) {
	sysDir := t.TempDir()
	dataHome := t.TempDir()
	origSystemDir, origGetUID := systemDir, getUID
	systemDir = sysDir
	getUID = func() int { return 1000 } // simulate a normal, non-root user
	defer func() { systemDir, getUID = origSystemDir, origGetUID }()
	t.Setenv("XDG_DATA_HOME", dataHome)

	if err := EnsureInstalled(); err != nil {
		t.Fatalf("EnsureInstalled failed: %v", err)
	}
	want := filepath.Join(dataHome, "applications", fileName)
	if !exists(want) {
		t.Fatalf("expected %s to be created", want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || !contains(string(data), "Exec=") {
		t.Errorf("unexpected desktop file content: %q", data)
	}
}

func TestEnsureInstalledCreatesSystemFileWhenRoot(t *testing.T) {
	sysDir := t.TempDir()
	dataHome := t.TempDir()
	origSystemDir, origGetUID := systemDir, getUID
	systemDir = sysDir
	getUID = func() int { return 0 } // simulate root
	defer func() { systemDir, getUID = origSystemDir, origGetUID }()
	t.Setenv("XDG_DATA_HOME", dataHome)

	if err := EnsureInstalled(); err != nil {
		t.Fatalf("EnsureInstalled failed: %v", err)
	}
	want := filepath.Join(sysDir, fileName)
	if !exists(want) {
		t.Fatalf("expected %s to be created", want)
	}
	userPath := filepath.Join(dataHome, "applications", fileName)
	if exists(userPath) {
		t.Errorf("did not expect a user-level file to also be created when running as root")
	}
}

func TestEnsureInstalledSkipsWhenSystemFileExists(t *testing.T) {
	sysDir := t.TempDir()
	dataHome := t.TempDir()
	origSystemDir, origGetUID := systemDir, getUID
	systemDir = sysDir
	getUID = func() int { return 1000 }
	defer func() { systemDir, getUID = origSystemDir, origGetUID }()
	t.Setenv("XDG_DATA_HOME", dataHome)

	if err := os.WriteFile(filepath.Join(sysDir, fileName), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureInstalled(); err != nil {
		t.Fatalf("EnsureInstalled failed: %v", err)
	}
	userPath := filepath.Join(dataHome, "applications", fileName)
	if exists(userPath) {
		t.Errorf("did not expect a user-level file to be created when a system one already exists")
	}
}

func TestEnsureInstalledSkipsWhenUserFileExists(t *testing.T) {
	sysDir := t.TempDir()
	dataHome := t.TempDir()
	origSystemDir, origGetUID := systemDir, getUID
	systemDir = sysDir
	getUID = func() int { return 1000 }
	defer func() { systemDir, getUID = origSystemDir, origGetUID }()
	t.Setenv("XDG_DATA_HOME", dataHome)

	appsDir := filepath.Join(dataHome, "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, fileName), []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureInstalled(); err != nil {
		t.Fatalf("EnsureInstalled failed: %v", err)
	}
	sysPath := filepath.Join(sysDir, fileName)
	if exists(sysPath) {
		t.Errorf("did not expect a system-level file to be created when a user one already exists")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
