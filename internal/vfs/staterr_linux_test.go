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

package vfs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// lockedFile makes dir/locked/file, then takes locked's x away: file's
// details become unreadable for a non-root caller.
func lockedFile(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores the missing x permission")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(locked, "file")
	if err := os.WriteFile(file, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	return file
}

// StatElevated runs stat(1) through pkexec and reads its line.
func TestStatElevatedRunsStatThroughPkexec(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	script := filepath.Join(t.TempDir(), "fake-pkexec.sh")
	content := "#!/bin/sh\n{ for a in \"$@\"; do printf '%s\\n' \"$a\"; done; echo ---; } >> " + shellQuote(logPath) +
		"\necho '43ff 0 0 4096 1700000000'\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	setPkexecPathForTest(t, script)

	e, err := NewLocalFS("Local", "/").StatElevated("/some/locked/tls")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "tls" || !e.IsDir || UnixMode(e.Mode) != 0o1777 || e.ModTime.Unix() != 1700000000 {
		t.Errorf("entry %+v", e)
	}
	want := []string{"stat", "-c", "%f %u %g %s %Y", "--", "/some/locked/tls"}
	if got := readLoggedArgs(t, logPath); !reflect.DeepEqual(got, want) {
		t.Errorf("pkexec args = %q, want %q", got, want)
	}
}

// chmod/chown of an entry whose details are hidden go straight to pkexec
// (a plain attempt can only fail the same way), with the special bits and
// a five-digit mode; both at once take a single elevation.
func TestHiddenEntryChangesGoElevated(t *testing.T) {
	file := lockedFile(t)
	logPath := filepath.Join(t.TempDir(), "log")
	fakePkexec(t, logPath, 0)
	l := NewLocalFS("Local", "/")

	if err := l.Chmod(file, 0o640|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	if got, want := readLoggedArgs(t, logPath), []string{"chmod", "02640", "--", file}; !reflect.DeepEqual(got, want) {
		t.Errorf("chmod via pkexec = %q, want %q", got, want)
	}
	if err := l.Chown(file, 0, 0); err != nil {
		t.Fatal(err)
	}
	if got := readLoggedArgs(t, logPath); len(got) < 1 || got[0] != "chown" {
		t.Errorf("chown via pkexec = %q", got)
	}
	if err := l.SetOwnerAndMode(file, 0, 0, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readLoggedArgs(t, logPath); len(got) < 6 || got[0] != "sh" || got[4] != "0:0" || got[5] != "00600" {
		t.Errorf("owner and mode via one pkexec = %q", got)
	}
}

func TestSearchUnlockModeAddsOnlyTheCallersBit(t *testing.T) {
	file := lockedFile(t)
	cur, want, ok := NewLocalFS("Local", "/").SearchUnlockMode(filepath.Dir(file))
	if !ok || cur.Perm() != 0o644 || want.Perm() != 0o744 || !want.IsDir() {
		t.Errorf("SearchUnlockMode = %v %v %v, want 644 -> 744", cur, want, ok)
	}
	if _, _, ok := NewLocalFS("Local", "/").SearchUnlockMode(t.TempDir()); ok {
		t.Error("an already searchable folder has nothing to unlock")
	}

	id := identity{euid: 1000, egid: 100, groups: map[uint32]bool{100: true}}
	for _, c := range []struct {
		uid, gid uint32
		want     os.FileMode
	}{{1000, 0, 0o100}, {0, 100, 0o010}, {0, 0, 0o001}} {
		if got := searchBit(c.uid, c.gid, id); got != c.want {
			t.Errorf("searchBit(%d, %d) = %o, want %o", c.uid, c.gid, got, c.want)
		}
	}
}
