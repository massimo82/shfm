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
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"shfm/internal/vfs"
)

// Every test talks to fake services (httptest servers in fake_*_test.go),
// never to the real ones: no account, no network.

const testToken = "test-access-token"

func TestMain(m *testing.M) {
	// Retries without the real waits.
	defaultRetry.base, defaultRetry.max = time.Millisecond, 5*time.Millisecond
	drivePolicy.base, drivePolicy.max = time.Millisecond, 5*time.Millisecond
	// Uploads of several chunks without big files.
	driveChunk = 64 << 10
	oneDriveSimpleMax = 100 << 10
	oneDriveChunk = 64 << 10
	dropboxChunk = 64 << 10
	os.Exit(m.Run())
}

// isolateConfig points the token store and secret's key seed at a
// temporary folder.
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

// testClient adds testToken to every request, as the real client adds
// the account's.
func testClient() *http.Client {
	return authClient(oauth2.StaticTokenSource(&oauth2.Token{AccessToken: testToken, TokenType: "Bearer"}))
}

// checkAuth fails a fake service's request without the test token.
func checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, `{"error":{"code":"InvalidAuthenticationToken","message":"no token"}}`, http.StatusUnauthorized)
		return false
	}
	return true
}

func testData(n int) []byte {
	b := make([]byte, n)
	r := rand.New(rand.NewPCG(uint64(n), 42))
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return b
}

func writeFile(t *testing.T, fs vfs.FileSystem, p string, data []byte) {
	t.Helper()
	w, err := fs.Create(p)
	if err != nil {
		t.Fatalf("Create(%s): %v", p, err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing %s: %v", p, err)
	}
}

func readFile(t *testing.T, fs vfs.FileSystem, p string) []byte {
	t.Helper()
	r, err := fs.Open(p)
	if err != nil {
		t.Fatalf("Open(%s): %v", p, err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading %s: %v", p, err)
	}
	return data
}

func names(entries []vfs.Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

// testConformance checks the behavior every cloud source must have, as
// the rest of shfm relies on it, working in folder base ("" for the
// root).
func testConformance(t *testing.T, fs vfs.FileSystem, base string) {
	if fs.Kind() != vfs.KindCloud || fs.Root() != max(base, "/") {
		t.Fatalf("Kind/Root = %v/%q, want root %q", fs.Kind(), fs.Root(), max(base, "/"))
	}

	// Folders.
	if err := fs.Mkdir(base + "/docs"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := fs.Mkdir(base + "/docs"); !errors.Is(err, os.ErrExist) {
		t.Errorf("Mkdir of an existing folder: %v, want os.ErrExist", err)
	}
	if err := fs.Mkdir(base + "/docs/sub dir"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	// Files: small, and spanning several upload chunks.
	small := []byte("hello, cloud")
	big := testData(300<<10 + 123)
	writeFile(t, fs, base+"/docs/small.txt", small)
	writeFile(t, fs, base+"/docs/sub dir/big.bin", big)
	if got := readFile(t, fs, base+"/docs/small.txt"); !bytes.Equal(got, small) {
		t.Errorf("small.txt = %q, want %q", got, small)
	}
	if got := readFile(t, fs, base+"/docs/sub dir/big.bin"); !bytes.Equal(got, big) {
		t.Errorf("big.bin differs (%d bytes read, %d written)", len(got), len(big))
	}
	if sc, ok := fs.(vfs.SizedCreator); ok {
		w, err := sc.CreateSized(base+"/docs/sized.bin", int64(len(big)))
		if err != nil {
			t.Fatal(err)
		}
		w.Write(big)
		if err := w.Close(); err != nil {
			t.Fatalf("CreateSized: %v", err)
		}
		if got := readFile(t, fs, base+"/docs/sized.bin"); !bytes.Equal(got, big) {
			t.Error("sized.bin differs")
		}
		fs.Remove(base + "/docs/sized.bin")
	}

	// Replacing a file's content.
	writeFile(t, fs, base+"/docs/small.txt", []byte("replaced"))
	if got := readFile(t, fs, base+"/docs/small.txt"); string(got) != "replaced" {
		t.Errorf("after replacing: %q", got)
	}
	entries, err := fs.List(base + "/docs")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(entries); strings.Join(got, ",") != "small.txt,sub dir" {
		t.Errorf("List(/docs) = %v (a replaced file must not be duplicated)", got)
	}

	// Stat.
	e, err := fs.Stat(base + "/docs/sub dir/big.bin")
	if err != nil || e.IsDir || e.Size != int64(len(big)) || e.Name != "big.bin" || !e.Mode.IsRegular() {
		t.Errorf("Stat(big.bin) = %+v, %v", e, err)
	}
	if e, err := fs.Stat(base + "/docs/sub dir"); err != nil || !e.IsDir {
		t.Errorf("Stat(sub dir) = %+v, %v", e, err)
	}
	if e, err := fs.Stat("/"); err != nil || !e.IsDir {
		t.Errorf("Stat(/) = %+v, %v", e, err)
	}
	if _, err := fs.Stat(base + "/docs/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat of a missing file: %v, want os.ErrNotExist", err)
	}
	if _, err := fs.List(base + "/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("List of a missing folder: %v, want os.ErrNotExist", err)
	}
	if vfs.IsConnectionFailure(func() error { _, err := fs.Stat(base + "/docs/missing"); return err }()) {
		t.Error("a missing file counted as a lost connection")
	}

	// Names that need escaping.
	odd := base + "/docs/odd #1 % & + ; ü 漢字.txt"
	writeFile(t, fs, odd, []byte("odd"))
	if got := readFile(t, fs, odd); string(got) != "odd" {
		t.Errorf("%s = %q", odd, got)
	}
	if _, err := fs.Stat(odd); err != nil {
		t.Errorf("Stat(%s): %v", odd, err)
	}

	// Empty files.
	if err := fs.CreateEmptyFile(base + "/docs/empty"); err != nil {
		t.Fatalf("CreateEmptyFile: %v", err)
	}
	if err := fs.CreateEmptyFile(base + "/docs/empty"); !errors.Is(err, os.ErrExist) {
		t.Errorf("CreateEmptyFile of an existing file: %v, want os.ErrExist", err)
	}
	if got := readFile(t, fs, base+"/docs/empty"); len(got) != 0 {
		t.Errorf("empty file has %d bytes", len(got))
	}
	writeFile(t, fs, base+"/docs/empty2", nil)
	if e, err := fs.Stat(base + "/docs/empty2"); err != nil || e.Size != 0 {
		t.Errorf("Stat(empty2) = %+v, %v", e, err)
	}

	// Random access reads (the FUSE mount's).
	ra := fs.(vfs.RandomAccessOpener)
	f, err := ra.OpenRandom(base+"/docs/sub dir/big.bin", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, off := range []int64{0, 4096, 8192, 100000, 10, int64(len(big)) - 100} {
		buf := make([]byte, 4096)
		n, err := f.ReadAt(buf, off)
		want := big[off:min(off+4096, int64(len(big)))]
		if (err != nil && err != io.EOF) || !bytes.Equal(buf[:n], want) {
			t.Errorf("ReadAt(%d) = %d, %v", off, n, err)
		}
	}
	if n, err := f.ReadAt(make([]byte, 10), int64(len(big))); n != 0 || err != io.EOF {
		t.Errorf("ReadAt(end) = %d, %v; want 0, EOF", n, err)
	}
	if _, err := f.WriteAt([]byte("x"), 0); err == nil {
		t.Error("WriteAt on a read-only open succeeded")
	}
	f.Close()

	// Random access writes: changed in place, uploaded on Close.
	f, err = ra.OpenRandom(base+"/docs/small.txt", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("RE"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close after writing: %v", err)
	}
	if got := readFile(t, fs, base+"/docs/small.txt"); string(got) != "REplaced" {
		t.Errorf("after WriteAt: %q", got)
	}
	f, _ = ra.OpenRandom(base+"/docs/small.txt", os.O_WRONLY, 0)
	if err := f.Truncate(2); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := readFile(t, fs, base+"/docs/small.txt"); string(got) != "RE" {
		t.Errorf("after Truncate: %q", got)
	}
	f, err = ra.OpenRandom(base+"/docs/new.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		t.Fatalf("OpenRandom(O_CREATE): %v", err)
	}
	if _, err := fs.Stat(base + "/docs/new.txt"); err != nil {
		t.Errorf("a file just created isn't there before Close: %v", err)
	}
	f.WriteAt([]byte("new"), 0)
	f.Close()
	if got := readFile(t, fs, base+"/docs/new.txt"); string(got) != "new" {
		t.Errorf("new.txt = %q", got)
	}
	if _, err := ra.OpenRandom(base+"/docs/new.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0); !errors.Is(err, os.ErrExist) {
		t.Errorf("O_EXCL on an existing file: %v", err)
	}
	f, _ = ra.OpenRandom(base+"/docs/new.txt", os.O_WRONLY|os.O_TRUNC, 0)
	f.WriteAt([]byte("t"), 0)
	f.Close()
	if got := readFile(t, fs, base+"/docs/new.txt"); string(got) != "t" {
		t.Errorf("after O_TRUNC: %q", got)
	}
	if _, err := ra.OpenRandom(base+"/docs", os.O_RDONLY, 0); err == nil {
		t.Error("OpenRandom on a folder succeeded")
	}

	// Rename and move.
	if err := fs.Rename(base+"/docs/new.txt", base+"/docs/renamed.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := fs.Stat(base + "/docs/new.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old name still there: %v", err)
	}
	if err := fs.Rename(base+"/docs/renamed.txt", base+"/docs/sub dir/moved.txt"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if got := readFile(t, fs, base+"/docs/sub dir/moved.txt"); string(got) != "t" {
		t.Errorf("moved.txt = %q", got)
	}
	if err := fs.Rename(base+"/docs/sub dir/moved.txt", base+"/docs/small.txt"); err != nil {
		t.Fatalf("Rename over an existing file: %v", err)
	}
	if got := readFile(t, fs, base+"/docs/small.txt"); string(got) != "t" {
		t.Errorf("after replacing by rename: %q", got)
	}
	if err := fs.Rename(base+"/docs/sub dir", base+"/moved dir"); err != nil {
		t.Fatalf("moving a folder: %v", err)
	}
	if got := readFile(t, fs, base+"/moved dir/big.bin"); !bytes.Equal(got, big) {
		t.Error("a moved folder's file differs")
	}
	if _, err := fs.List(base + "/docs/sub dir"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("moved folder still listed at its old path: %v", err)
	}

	// Modification times, where the service keeps them.
	if ts, ok := fs.(vfs.TimesSetter); ok {
		when := time.Date(2020, 5, 17, 10, 30, 0, 0, time.UTC)
		if err := ts.Chtimes(base+"/docs/small.txt", time.Time{}, when); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
		if e, _ := fs.Stat(base + "/docs/small.txt"); !e.ModTime.Equal(when) {
			t.Errorf("mtime = %v, want %v", e.ModTime, when)
		}
	}

	// Space.
	if sr, ok := fs.(vfs.SpaceReporter); ok {
		if total, free, err := sr.Space("/"); err != nil || total == 0 || free > total {
			t.Errorf("Space = %d, %d, %v", total, free, err)
		}
	}

	// Remove, recursively.
	if err := fs.Remove(base + "/docs/small.txt"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove(base + "/moved dir"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(base + "/moved dir/big.bin"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a removed folder's file is still there: %v", err)
	}
	if err := fs.Remove(base + "/docs/missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Remove of a missing file: %v", err)
	}
	if err := fs.Remove("/"); err == nil {
		t.Error("removing the root succeeded")
	}
}

// TestFSVariants: each backend's FS implements exactly the optional
// interfaces it supports.
func TestFSVariants(t *testing.T) {
	for _, c := range []struct {
		caps         caps
		times, sized bool
	}{
		{caps{streamUpload: true, modTime: true}, true, false},
		{caps{streamUpload: true}, false, false},
		{caps{modTime: true}, true, true},
		{caps{}, false, true},
	} {
		fs := newFS(Account{Provider: Dropbox, User: "u"}, nil, c.caps)
		_, times := fs.(vfs.TimesSetter)
		_, sized := fs.(vfs.SizedCreator)
		if times != c.times || sized != c.sized {
			t.Errorf("%+v: TimesSetter %v, SizedCreator %v; want %v, %v", c.caps, times, sized, c.times, c.sized)
		}
		if fs.Label() != "dropbox://u" {
			t.Errorf("Label = %q", fs.Label())
		}
	}
}

// TestAPIErrorClassification: errors from the services map onto the os
// errors, and never count as a lost connection.
func TestAPIErrorClassification(t *testing.T) {
	for status, want := range map[int]error{404: os.ErrNotExist, 409: os.ErrExist, 403: os.ErrPermission, 401: ErrAuthorization} {
		err := driveParseErr(status, []byte(`{"error":{"message":"m","errors":[{"reason":"r"}]}}`))
		if !errors.Is(err, want) {
			t.Errorf("status %d: %v is not %v", status, err, want)
		}
		if vfs.IsConnectionFailure(pathErr("stat", "/x", err)) {
			t.Errorf("status %d counted as a lost connection", status)
		}
	}
	if err := driveParseErr(403, []byte(`{"error":{"errors":[{"reason":"userRateLimitExceeded"}]}}`)); errors.Is(err, os.ErrPermission) {
		t.Error("throttling reported as access denied")
	}
}

// TestTokenStore: tokens survive a round trip through the encrypted file,
// which is private to the user.
func TestTokenStore(t *testing.T) {
	isolateConfig(t)
	tok := &oauth2.Token{AccessToken: "a", RefreshToken: "r", Expiry: time.Now().Add(time.Hour).Round(0)}
	if err := saveToken("acc1", tok); err != nil {
		t.Fatal(err)
	}
	if err := saveToken("acc2", &oauth2.Token{AccessToken: "b"}); err != nil {
		t.Fatal(err)
	}
	got, err := loadToken("acc1")
	if err != nil || got.AccessToken != "a" || got.RefreshToken != "r" || !got.Expiry.Equal(tok.Expiry) {
		t.Errorf("loadToken = %+v, %v", got, err)
	}
	if _, err := loadToken("nope"); !errors.Is(err, ErrAuthorization) {
		t.Errorf("loading an unknown account: %v", err)
	}
	p, _ := tokensPath()
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("token file: %v, %v", info.Mode(), err)
	}
	data, _ := os.ReadFile(p)
	if bytes.Contains(data, []byte(`"r"`)) || bytes.Contains(data, []byte("RefreshToken")) {
		t.Error("the token file holds a token in plain text")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(p), ".cloud-tokens-*")); len(leftovers) > 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

// TestForget: a removed account's token is gone, and a source still open
// on it refreshing its token doesn't bring it back.
func TestForget(t *testing.T) {
	isolateConfig(t)
	saveToken("gone", &oauth2.Token{AccessToken: "a", RefreshToken: "r"})
	saveToken("kept", &oauth2.Token{AccessToken: "b", RefreshToken: "s"})
	if err := Forget("gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := loadToken("gone"); !errors.Is(err, ErrAuthorization) {
		t.Errorf("forgotten token still loads: %v", err)
	}
	saveToken("gone", &oauth2.Token{AccessToken: "refreshed", RefreshToken: "r2"})
	if _, err := loadToken("gone"); !errors.Is(err, ErrAuthorization) {
		t.Errorf("a refresh brought the forgotten token back: %v", err)
	}
	if tok, err := loadToken("kept"); err != nil || tok.AccessToken != "b" {
		t.Errorf("another account's token: %+v, %v", tok, err)
	}
	if err := Forget("never-saved"); err != nil {
		t.Errorf("Forget of an unknown account: %v", err)
	}
}
