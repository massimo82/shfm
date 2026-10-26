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
	"time"
)

// ListElevated runs a single find(1) through pkexec, with the format
// parseFindList reads.
func TestListElevatedRunsFindThroughPkexec(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log")
	fakePkexec(t, logPath, 0)
	if _, err := NewLocalFS("Local", "/").ListElevated("/srv/locked"); err != nil {
		t.Fatal(err)
	}
	want := []string{"find", "-H", "/srv/locked", "-mindepth", "1", "-maxdepth", "1", "-printf", findListFormat}
	if got := readLoggedArgs(t, logPath); !reflect.DeepEqual(got, want) {
		t.Errorf("pkexec args = %q, want %q", got, want)
	}
}

// The real find(1), run by a pkexec stand-in that just executes it (as the
// user, on a folder they can read), gives what parseFindList expects:
// types, symlinks to folders, special bits, names with spaces and newlines.
func TestListElevatedReadsRealFind(t *testing.T) {
	script := filepath.Join(t.TempDir(), "exec-pkexec.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ \"$1\" = --disable-internal-agent ] && shift\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	setPkexecPathForTest(t, script)

	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	if err := os.Chmod(filepath.Join(dir, "sub"), 0o750|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	odd := "a file\nwith newline"
	os.WriteFile(filepath.Join(dir, odd), []byte("12345"), 0o640)
	os.Symlink("sub", filepath.Join(dir, "link"))
	os.Symlink("missing", filepath.Join(dir, "broken"))
	mtime := time.Unix(1700000000, 500000000)
	os.Chtimes(filepath.Join(dir, odd), mtime, mtime)

	entries, err := NewLocalFS("Local", "/").ListElevated(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Entry{}
	for _, e := range entries {
		got[e.Name] = e
	}
	if len(got) != 4 {
		t.Fatalf("entries = %+v", entries)
	}
	if e := got["sub"]; !e.IsDir || e.IsSymlink || UnixMode(e.Mode) != 0o2750 || e.Owner == "" {
		t.Errorf("sub = %+v", e)
	}
	if e := got[odd]; e.IsDir || e.Size != 5 || e.Mode != 0o640 || !e.ModTime.Equal(mtime) {
		t.Errorf("file = %+v", e)
	}
	if e := got["link"]; !e.IsSymlink || !e.IsDir {
		t.Errorf("link to a folder = %+v", e)
	}
	if e := got["broken"]; !e.IsSymlink || e.IsDir {
		t.Errorf("broken link = %+v", e)
	}
}

func TestParseFindListRejectsGarbage(t *testing.T) {
	if _, err := parseFindList("f f 644 0 0 notanumber 1.0 x\x00"); err == nil {
		t.Error("a malformed record should be an error")
	}
	if e, err := parseFindList(""); err != nil || len(e) != 0 {
		t.Errorf("an empty folder = %v %v", e, err)
	}
}

// A folder the user can't list gets the r and x bits of their class.
func TestListUnlockModeAddsReadAndSearch(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root lists any folder")
	}
	dir := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ mode, want os.FileMode }{{0o000, 0o500}, {0o300, 0o700}, {0o600 | os.ModeSticky, 0o700 | os.ModeSticky}} {
		if err := os.Chmod(dir, c.mode); err != nil {
			t.Fatal(err)
		}
		cur, want, ok := NewLocalFS("Local", "/").ListUnlockMode(dir)
		if !ok || UnixMode(cur) != UnixMode(c.mode) || UnixMode(want) != UnixMode(c.want) {
			t.Errorf("ListUnlockMode(%o) = %o %o %v, want %o", c.mode, UnixMode(cur), UnixMode(want), ok, c.want)
		}
	}
	os.Chmod(dir, 0o755)
	if _, _, ok := NewLocalFS("Local", "/").ListUnlockMode(dir); ok {
		t.Error("a listable folder has nothing to unlock")
	}
}
