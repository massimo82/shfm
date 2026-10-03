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
	"io"
	"os"
	"strings"
	"testing"

	"shfm/internal/vfs"
)

func newTestDriveFS(t *testing.T) (*fakeDrive, vfs.FileSystem) {
	f := newFakeDrive(t)
	fs := newFS(Account{Provider: GoogleDrive, User: "user@example.com"}, newDrive(testClient()), providers[GoogleDrive].caps)
	t.Cleanup(func() { fs.Close() })
	return f, fs
}

func TestDriveConformance(t *testing.T) {
	_, fs := newTestDriveFS(t)
	testConformance(t, fs, "/My Drive")
}

// TestDriveGoogleDocs: Google documents are listed under their export
// format's extension, read as that format, and can't be written.
func TestDriveGoogleDocs(t *testing.T) {
	f, fs := newTestDriveFS(t)
	f.add("root", "Report", "application/vnd.google-apps.document", nil)
	f.add("root", "Survey", "application/vnd.google-apps.form", nil)

	entries, err := fs.List("/My Drive")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(entries), ","); got != "Report.odt" {
		t.Fatalf("List = %s; want only Report.odt (a form can't be exported)", got)
	}
	e, err := fs.Stat("/My Drive/Report.odt")
	if err != nil || !e.SizeUnknown || e.Mode.Perm() != 0o444 {
		t.Errorf("Stat(Report.odt) = %+v, %v", e, err)
	}
	want := "Report as application/vnd.oasis.opendocument.text"
	if got := string(readFile(t, fs, "/My Drive/Report.odt")); got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	ra := fs.(vfs.RandomAccessOpener)
	rf, err := ra.OpenRandom("/My Drive/Report.odt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	if n, err := rf.ReadAt(buf, 7); (err != nil && err != io.EOF) || string(buf[:n]) != want[7:13] {
		t.Errorf("ReadAt(7) = %q, %v", buf[:n], err)
	}
	rf.Close()

	w, _ := fs.Create("/My Drive/Report.odt")
	w.Write([]byte("x"))
	if err := w.Close(); !errors.Is(err, os.ErrPermission) {
		t.Errorf("writing a Google document: %v, want os.ErrPermission", err)
	}
	if err := fs.Rename("/My Drive/Report.odt", "/My Drive/Final.odt"); err != nil {
		t.Fatalf("renaming a Google document: %v", err)
	}
	for _, file := range f.files {
		if file.mime == "application/vnd.google-apps.document" && file.name != "Final" {
			t.Errorf("renamed to %q, want Final (no export extension)", file.name)
		}
	}
}

// TestDriveShortcuts: a shortcut is a link to its target, navigable when
// it's a folder.
func TestDriveShortcuts(t *testing.T) {
	f, fs := newTestDriveFS(t)
	target := f.add("root", "Shared stuff", driveFolderMime, nil)
	f.add(target.id, "inside.txt", "", []byte("inside"))
	link := f.add("root", "Link", driveShortcutMime, nil)
	link.target = target.id

	e, err := fs.Stat("/My Drive/Link")
	if err != nil || !e.IsDir || !e.IsSymlink {
		t.Fatalf("Stat(Link) = %+v, %v", e, err)
	}
	if got := string(readFile(t, fs, "/My Drive/Link/inside.txt")); got != "inside" {
		t.Errorf("through the shortcut: %q", got)
	}
	if err := fs.Remove("/My Drive/Link"); err != nil {
		t.Fatal(err)
	}
	if target.trashed || !link.trashed {
		t.Errorf("removing a shortcut must trash it, not its target (target trashed %v, link %v)", target.trashed, link.trashed)
	}
}

// TestDriveNames: a '/' in a Drive name is shown as '／'; of several
// files with the same name, the newest is the one the path means.
func TestDriveNames(t *testing.T) {
	f, fs := newTestDriveFS(t)
	f.add("root", "a/b", "", []byte("slash"))
	f.add("root", "dup", "", []byte("old"))
	f.add("root", "dup", "", []byte("new"))

	if got := string(readFile(t, fs, "/My Drive/a／b")); got != "slash" {
		t.Errorf("a／b = %q", got)
	}
	writeFile(t, fs, "/My Drive/c／d", []byte("x"))
	found := false
	for _, file := range f.files {
		found = found || file.name == "c/d"
	}
	if !found {
		t.Error("a '／' written isn't a '/' in the Drive name")
	}
	if got := string(readFile(t, fs, "/My Drive/dup")); got != "new" {
		t.Errorf("dup = %q, want the newest", got)
	}
}

// TestDriveStaleFolderCache: a folder moved by another client is found
// again at its new path, and no longer at its old one, once its cached ID
// has expired.
func TestDriveStaleFolderCache(t *testing.T) {
	f, fs := newTestDriveFS(t)
	dir := f.add("root", "old", driveFolderMime, nil)
	f.add(dir.id, "f.txt", "", []byte("f"))
	if got := string(readFile(t, fs, "/My Drive/old/f.txt")); got != "f" {
		t.Fatalf("f.txt = %q", got)
	}
	f.mu.Lock()
	dir.name = "new"
	f.mu.Unlock()
	// Changes made elsewhere show once the cached IDs expire.
	old := driveDirTTL
	driveDirTTL = 0
	t.Cleanup(func() { driveDirTTL = old })
	if got := string(readFile(t, fs, "/My Drive/new/f.txt")); got != "f" {
		t.Errorf("after the rename elsewhere: %q", got)
	}
	f.add("root", "old", driveFolderMime, nil)
	if entries, err := fs.List("/My Drive/old"); err != nil || len(entries) != 0 {
		t.Errorf("List(/old) = %v, %v; want the new, empty folder", names(entries), err)
	}
}

// TestDriveThrottling: rate-limited requests are retried.
func TestDriveThrottling(t *testing.T) {
	f, fs := newTestDriveFS(t)
	f.throttle = 3
	if _, err := fs.List("/My Drive"); err != nil {
		t.Fatalf("List after throttling: %v", err)
	}
	f.throttle = 100
	if _, err := fs.List("/My Drive"); err == nil {
		t.Error("endless throttling didn't fail")
	}
}

// TestDrivePlaces: the account's root holds My Drive, the shared drives
// and what's shared with the account, each browsable and writable inside;
// the places, shared drives and shared items themselves are fixed.
func TestDrivePlaces(t *testing.T) {
	f, fs := newTestDriveFS(t)
	if fs.Root() != "/My Drive" {
		t.Errorf("Root = %q", fs.Root())
	}
	f.addSharedDrive("sd1", "Team")
	f.add("sd1", "plan.txt", "", []byte("plan"))
	f.addShared("from Ann.txt", "", []byte("hello"))
	folder := f.addShared("Ann's folder", driveFolderMime, nil)
	f.add(folder.id, "inside.txt", "", []byte("inside"))
	f.add("root", "mine.txt", "", []byte("mine"))

	list := func(p string) string {
		t.Helper()
		entries, err := fs.List(p)
		if err != nil {
			t.Fatalf("List(%s): %v", p, err)
		}
		return strings.Join(names(entries), ",")
	}
	if got := list("/"); got != "My Drive,Shared drives,Shared with me" {
		t.Errorf("root: %s", got)
	}
	if got := list("/Shared drives"); got != "Team" {
		t.Errorf("Shared drives: %s", got)
	}
	if got := list("/Shared drives/Team"); got != "plan.txt" {
		t.Errorf("Team: %s", got)
	}
	if got := list("/Shared with me"); got != "Ann's folder,from Ann.txt" {
		t.Errorf("Shared with me: %s", got)
	}
	if got := list("/My Drive"); got != "mine.txt" {
		t.Errorf("My Drive: %s (shared items don't belong there)", got)
	}
	for p, want := range map[string]string{
		"/Shared drives/Team/plan.txt":            "plan",
		"/Shared with me/from Ann.txt":            "hello",
		"/Shared with me/Ann's folder/inside.txt": "inside",
	} {
		if got := string(readFile(t, fs, p)); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
	}

	// Writing inside a shared drive and a shared folder.
	writeFile(t, fs, "/Shared drives/Team/new.txt", []byte("new"))
	if err := fs.Mkdir("/Shared drives/Team/sub"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, fs, "/Shared drives/Team/sub/deep.txt", []byte("deep"))
	writeFile(t, fs, "/Shared with me/Ann's folder/reply.txt", []byte("reply"))
	for _, file := range f.files {
		if file.name == "new.txt" && file.driveID != "sd1" {
			t.Errorf("new.txt created outside the shared drive (driveId %q)", file.driveID)
		}
	}
	if got := list("/Shared drives/Team/sub"); got != "deep.txt" {
		t.Errorf("Team/sub: %s", got)
	}
	if got := list("/Shared with me/Ann's folder"); got != "inside.txt,reply.txt" {
		t.Errorf("Ann's folder: %s", got)
	}
	if err := fs.Rename("/My Drive/mine.txt", "/Shared drives/Team/mine.txt"); err != nil {
		t.Fatalf("moving into a shared drive: %v", err)
	}
	if got := string(readFile(t, fs, "/Shared drives/Team/mine.txt")); got != "mine" {
		t.Errorf("moved file: %q", got)
	}

	// The fixed entries.
	for _, p := range []string{"/My Drive", "/Shared drives", "/Shared with me", "/Shared drives/Team", "/Shared with me/from Ann.txt"} {
		if err := fs.Remove(p); !errors.Is(err, os.ErrPermission) {
			t.Errorf("Remove(%s): %v, want os.ErrPermission", p, err)
		}
		if err := fs.Rename(p, "/My Drive/renamed"); !errors.Is(err, os.ErrPermission) {
			t.Errorf("Rename(%s): %v, want os.ErrPermission", p, err)
		}
	}
	for _, p := range []string{"/new", "/Shared drives/new", "/Shared with me/new"} {
		if err := fs.Mkdir(p); !errors.Is(err, os.ErrPermission) {
			t.Errorf("Mkdir(%s): %v, want os.ErrPermission", p, err)
		}
	}
	if err := fs.Rename("/My Drive/dummy", "/Shared drives/Other"); err == nil {
		t.Error("renamed into a place")
	}
	if _, err := fs.Stat("/Elsewhere"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(/Elsewhere): %v", err)
	}
	if !strings.Contains(list("/Shared with me"), "from Ann.txt") {
		t.Error("a shared item went away")
	}
}
