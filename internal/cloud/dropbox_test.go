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
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/oauth2"

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

// TestDropboxMissingScope: an authorization lacking a permission (issued
// before the app got it) is one to renew, already when the account is
// opened; an app lacking it says where to give it.
func TestDropboxMissingScope(t *testing.T) {
	isolateConfig(t)
	f, fs := newTestDropboxFS(t)
	f.tokenLacks = "files.metadata.read"
	_, err := fs.List("/")
	if !errors.Is(err, ErrAuthorization) || !strings.Contains(err.Error(), "files.metadata.read") ||
		!strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("List without the permission: %v, want ErrAuthorization naming it", err)
	}

	acc := Account{Provider: Dropbox, ID: "acc", ClientID: "key"}
	saveToken("acc", &oauth2.Token{AccessToken: testToken, RefreshToken: "r"})
	if _, err := Dial(acc); !errors.Is(err, ErrAuthorization) {
		t.Errorf("Dial without the permission: %v, want ErrAuthorization", err)
	}

	f.tokenLacks, f.appLacks = "", "files.metadata.read"
	_, err = Dial(acc)
	if err == nil || errors.Is(err, ErrAuthorization) || !strings.Contains(err.Error(), "App Console") ||
		strings.Contains(err.Error(), "Error in call") || !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("Dial with an app without the permission: %v", err)
	}

	f.appLacks = ""
	if _, err := Dial(acc); err != nil {
		t.Errorf("Dial with every permission: %v", err)
	}
}

// TestDropboxErrorStatus: a route error doesn't claim an HTTP status the
// SDK didn't keep.
func TestDropboxErrorStatus(t *testing.T) {
	_, fs := newTestDropboxFS(t)
	_, err := fs.List("/missing")
	if !errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "HTTP 0") {
		t.Errorf("List of a missing folder: %v", err)
	}
}
