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

//go:build cloud

package cloud

import (
	"testing"

	"shfm/internal/vfs"
)

func newTestDropboxFS(t *testing.T) (*fakeDropbox, vfs.FileSystem) {
	f := newFakeDropbox(t)
	fs := newFS(Account{Provider: Dropbox, User: "user@example.com"}, newDropbox(testClient()), providers[Dropbox].caps)
	t.Cleanup(func() { fs.Close() })
	return f, fs
}

func TestDropboxConformance(t *testing.T) {
	_, fs := newTestDropboxFS(t)
	testConformance(t, fs, "")
}

// TestDropboxThrottling: the SDK retries throttled requests.
func TestDropboxThrottling(t *testing.T) {
	f, fs := newTestDropboxFS(t)
	f.throttle = 2
	if _, err := fs.List("/"); err != nil {
		t.Fatalf("List after throttling: %v", err)
	}
}

// TestDropboxCaseOnlyRename: renaming "a" to "A" isn't replacing a file
// with itself (Dropbox ignores case).
func TestDropboxCaseOnlyRename(t *testing.T) {
	_, fs := newTestDropboxFS(t)
	writeFile(t, fs, "/name.txt", []byte("keep me"))
	if err := fs.Rename("/name.txt", "/NAME.txt"); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, fs, "/NAME.txt")); got != "keep me" {
		t.Errorf("after a case-only rename: %q", got)
	}
}
