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

	"shfm/internal/vfs"
)

func newTestOneDriveFS(t *testing.T) (*fakeGraph, vfs.FileSystem) {
	f := newFakeGraph(t)
	fs := newFS(Account{Provider: OneDrive, User: "user@outlook.com"}, newOneDrive(testClient()), providers[OneDrive].caps)
	t.Cleanup(func() { fs.Close() })
	return f, fs
}

func TestOneDriveConformance(t *testing.T) {
	f, fs := newTestOneDriveFS(t)
	testConformance(t, fs, "/My files")
	if f.tokenOnPreauth {
		t.Error("the account's token was sent to a pre-authenticated download or upload URL")
	}
}

// TestOneDriveListPages: a folder listed over several pages comes back
// whole.
func TestOneDriveListPages(t *testing.T) {
	f, fs := newTestOneDriveFS(t)
	for i := range 7 {
		f.add(f.root(fakeMyDrive), string(rune('a'+i)), false, []byte{byte(i)})
	}
	entries, err := fs.List("/My files")
	if err != nil || len(entries) != 7 {
		t.Errorf("List = %v, %v; want 7 entries", names(entries), err)
	}
}

// TestOneDriveCaseOnlyRename: renaming "a" to "A" isn't replacing a file
// with itself (OneDrive ignores case).
func TestOneDriveCaseOnlyRename(t *testing.T) {
	_, fs := newTestOneDriveFS(t)
	writeFile(t, fs, "/My files/name.txt", []byte("keep me"))
	if err := fs.Rename("/My files/name.txt", "/My files/NAME.txt"); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, fs, "/My files/NAME.txt")); got != "keep me" {
		t.Errorf("after a case-only rename: %q", got)
	}
}

func listNames(t *testing.T, fs vfs.FileSystem, p string) string {
	t.Helper()
	entries, err := fs.List(p)
	if err != nil {
		t.Fatalf("List(%s): %v", p, err)
	}
	return strings.Join(names(entries), ",")
}

// TestOneDriveLinks: a shared folder added to My files opens as a link,
// even reached without listing its folder first; removing the link leaves
// the shared folder alone.
func TestOneDriveLinks(t *testing.T) {
	f, fs := newTestOneDriveFS(t)
	if fs.Root() != "/My files" {
		t.Errorf("Root = %q", fs.Root())
	}
	ann := f.addDrive("ANN")
	trip := f.add(ann, "Trip", true, nil)
	f.add(trip, "photo.jpg", false, []byte("jpeg"))
	day1 := f.add(trip, "day1", true, nil)
	f.add(day1, "a.txt", false, []byte("a"))
	f.addLink(f.root(fakeMyDrive), trip)
	f.add(f.root(fakeMyDrive), "mine.txt", false, []byte("mine"))

	if got := listNames(t, fs, "/"); got != "My files" {
		t.Errorf("root of a personal account: %s", got)
	}
	if _, err := fs.Stat("/Shared"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(/Shared) on a personal account: %v", err)
	}
	// Straight to a file behind the link, before anything listed it.
	if got := string(readFile(t, fs, "/My files/Trip/day1/a.txt")); got != "a" {
		t.Errorf("a.txt = %q", got)
	}
	e, err := fs.Stat("/My files/Trip")
	if err != nil || !e.IsDir || !e.IsSymlink {
		t.Errorf("Stat(Trip) = %+v, %v", e, err)
	}
	if got := listNames(t, fs, "/My files/Trip"); got != "day1,photo.jpg" {
		t.Errorf("Trip: %s", got)
	}

	writeFile(t, fs, "/My files/Trip/new.txt", []byte("new"))
	if err := fs.Mkdir("/My files/Trip/day2"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Rename("/My files/Trip/new.txt", "/My files/Trip/day2/new.txt"); err != nil {
		t.Fatalf("moving within the shared folder: %v", err)
	}
	if got := listNames(t, fs, "/My files/Trip/day2"); got != "new.txt" {
		t.Errorf("day2: %s", got)
	}
	if n := f.walk(trip, "day2/new.txt"); n == nil || n.drive != "ANN" {
		t.Error("new.txt isn't in the shared folder's drive")
	}
	if err := fs.Rename("/My files/mine.txt", "/My files/Trip/mine.txt"); !errors.Is(err, vfs.ErrNotSupported) {
		t.Errorf("moving between drives: %v, want vfs.ErrNotSupported (copy and delete instead)", err)
	}

	if err := fs.Remove("/My files/Trip"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.items[trip.id]; !ok {
		t.Error("removing the link removed the shared folder")
	}
	if got := listNames(t, fs, "/My files"); got != "mine.txt" {
		t.Errorf("My files after removing the link: %s", got)
	}

	if err := fs.Mkdir("/elsewhere"); !errors.Is(err, os.ErrPermission) {
		t.Errorf("Mkdir at the root: %v", err)
	}
	if err := fs.Remove("/My files"); !errors.Is(err, os.ErrPermission) {
		t.Errorf("Remove(/My files): %v", err)
	}
}

// TestOneDriveShared: a work or school account's Shared lists what others
// shared from their OneDrive, found through Microsoft Search: the shared
// items, not what's inside shared folders.
func TestOneDriveShared(t *testing.T) {
	f, fs := newTestOneDriveFS(t)
	f.driveType = "business"
	ann, bob := f.addDrive("ANN"), f.addDrive("BOB")
	plans := f.add(ann, "Plans", true, nil)
	plans.shared = true
	f.add(plans, "q1.txt", false, []byte("q1"))
	f.add(ann, "notes.txt", false, []byte("ann's")).shared = true
	f.add(ann, "private.txt", false, []byte("no"))
	f.add(bob, "notes.txt", false, []byte("bob's")).shared = true

	if got := listNames(t, fs, "/"); got != "My files,Shared" {
		t.Errorf("root of a work account: %s", got)
	}
	got := listNames(t, fs, "/Shared")
	if got != "Plans,notes (2).txt,notes.txt" {
		t.Errorf("Shared: %s", got)
	}
	if got := string(readFile(t, fs, "/Shared/Plans/q1.txt")); got != "q1" {
		t.Errorf("q1.txt = %q", got)
	}
	writeFile(t, fs, "/Shared/Plans/q2.txt", []byte("q2"))
	if n := f.walk(plans, "q2.txt"); n == nil || n.drive != "ANN" {
		t.Error("q2.txt isn't in the shared folder")
	}
	if err := fs.Remove("/Shared/Plans/q2.txt"); err != nil {
		t.Errorf("removing inside a shared folder: %v", err)
	}
	for _, p := range []string{"/Shared", "/Shared/Plans", "/Shared/notes.txt"} {
		if err := fs.Remove(p); !errors.Is(err, os.ErrPermission) {
			t.Errorf("Remove(%s): %v", p, err)
		}
		if err := fs.Rename(p, "/My files/x"); !errors.Is(err, os.ErrPermission) {
			t.Errorf("Rename(%s): %v", p, err)
		}
	}
	if err := fs.Mkdir("/Shared/new"); !errors.Is(err, os.ErrPermission) {
		t.Errorf("Mkdir in Shared: %v", err)
	}
	searches := f.searches
	listNames(t, fs, "/Shared")
	if f.searches != searches {
		t.Error("Shared searched again right away")
	}
}
