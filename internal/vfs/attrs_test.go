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

package vfs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// withAttrs makes readAttrs report the given flags for the given paths (and
// "no flags" for every other path), since setting real chattr flags needs
// root.
func withAttrs(t *testing.T, flags map[string]attrSet) {
	t.Helper()
	old := readAttrs
	readAttrs = func(path string) (attrSet, bool) { return flags[path], true }
	t.Cleanup(func() { readAttrs = old })
}

func wantAttrErr(t *testing.T, err error, path, attr string) {
	t.Helper()
	var ae *AttrError
	if !errors.As(err, &ae) {
		t.Fatalf("got %v, want an *AttrError for %s", err, path)
	}
	if ae.Path != path || ae.Attr != attr {
		t.Errorf("AttrError = {%s %s}, want {%s %s}", ae.Path, ae.Attr, path, attr)
	}
}

func TestCheckRemovable(t *testing.T) {
	const dir, file = "/x/dir", "/x/dir/file"
	tests := []struct {
		name  string
		flags map[string]attrSet
		want  string // path of the expected AttrError, "" for none
		attr  string
	}{
		{"no flags", nil, "", ""},
		{"immutable file", map[string]attrSet{file: attrImmutable}, file, "immutable"},
		{"append-only file", map[string]attrSet{file: attrAppendOnly}, file, "append-only"},
		{"immutable directory", map[string]attrSet{dir: attrImmutable}, dir, "immutable"},
		{"append-only directory", map[string]attrSet{dir: attrAppendOnly}, dir, "append-only"},
	}
	for _, tc := range tests {
		withAttrs(t, tc.flags)
		err := CheckRemovable(file)
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		wantAttrErr(t, err, tc.want, tc.attr)
	}

	// Flags that can't be read never block anything.
	old := readAttrs
	readAttrs = func(string) (attrSet, bool) { return attrImmutable, false }
	defer func() { readAttrs = old }()
	if err := CheckRemovable(file); err != nil {
		t.Errorf("unreadable flags must not block: %v", err)
	}
}

func TestCheckChangeable(t *testing.T) {
	const dir, file = "/x/dir", "/x/dir/file"
	withAttrs(t, map[string]attrSet{file: attrImmutable})
	wantAttrErr(t, checkChangeable(file), file, "immutable")

	withAttrs(t, map[string]attrSet{file: attrAppendOnly})
	wantAttrErr(t, checkChangeable(file), file, "append-only")

	// chmod/chown of a file inside an immutable directory is fine: only the
	// file's own flags matter.
	withAttrs(t, map[string]attrSet{dir: attrImmutable})
	if err := checkChangeable(file); err != nil {
		t.Errorf("a file's mode can change inside an immutable directory: %v", err)
	}
}

func TestCheckRenamable(t *testing.T) {
	tmp := t.TempDir()
	srcDir, dstDir := filepath.Join(tmp, "src"), filepath.Join(tmp, "dst")
	src, dst := filepath.Join(srcDir, "a"), filepath.Join(dstDir, "b")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}

	withAttrs(t, nil)
	if err := checkRenamable(src, dst); err != nil {
		t.Errorf("no flags: %v", err)
	}
	withAttrs(t, map[string]attrSet{src: attrImmutable})
	wantAttrErr(t, checkRenamable(src, dst), src, "immutable")
	withAttrs(t, map[string]attrSet{srcDir: attrAppendOnly})
	wantAttrErr(t, checkRenamable(src, dst), srcDir, "append-only")
	withAttrs(t, map[string]attrSet{dstDir: attrImmutable})
	wantAttrErr(t, checkRenamable(src, dst), dstDir, "immutable")

	// Adding an entry to an append-only directory is what append-only still
	// allows...
	withAttrs(t, map[string]attrSet{dstDir: attrAppendOnly})
	if err := checkRenamable(src, dst); err != nil {
		t.Errorf("moving into an append-only directory should be allowed: %v", err)
	}
	// ...but replacing an entry that already exists there deletes it.
	if err := os.WriteFile(dst, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wantAttrErr(t, checkRenamable(src, dst), dstDir, "append-only")
	withAttrs(t, map[string]attrSet{dst: attrImmutable})
	wantAttrErr(t, checkRenamable(src, dst), dst, "immutable")
}

func TestAttrErrorMessage(t *testing.T) {
	msg := (&AttrError{Path: "/a/b", Attr: "immutable"}).Error()
	for _, want := range []string{"/a/b", "immutable", "chattr +i", "chattr -i -- /a/b", "not even as root"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q should contain %q", msg, want)
		}
	}
	msg = (&AttrError{Path: "/a/b", Attr: "append-only"}).Error()
	for _, want := range []string{"append-only", "chattr +a", "chattr -a -- /a/b"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q should contain %q", msg, want)
		}
	}
}

func TestAttrSetNames(t *testing.T) {
	if got := (attrImmutable | attrAppendOnly).names(); len(got) != 2 || got[0] != "immutable" || got[1] != "append-only" {
		t.Errorf("names = %v", got)
	}
	if got := attrSet(0).names(); len(got) != 0 {
		t.Errorf("no flags should give no names, got %v", got)
	}
}

func TestRemoveAttrError(t *testing.T) {
	const nested = "/x/dir/sub/file"
	withAttrs(t, map[string]attrSet{nested: attrImmutable})

	permErr := &fs.PathError{Op: "unlink", Path: nested, Err: syscall.EPERM}
	wantAttrErr(t, removeAttrError(permErr), nested, "immutable")

	if removeAttrError(nil) != nil {
		t.Error("nil error must give nil")
	}
	if removeAttrError(&fs.PathError{Op: "unlink", Path: nested, Err: syscall.ENOENT}) != nil {
		t.Error("a non-permission error must not be turned into an AttrError")
	}
	if removeAttrError(&fs.PathError{Op: "unlink", Path: "/x/dir/sub/other", Err: syscall.EPERM}) != nil {
		t.Error("a permission error on an unflagged entry must be left alone")
	}
	if removeAttrError(errors.New("permission denied")) != nil {
		t.Error("an error that isn't a *PathError must be left alone")
	}
}

// The local backend refuses up front — with the flag named — instead of
// attempting the operation or handing it to pkexec, and leaves the entry
// untouched.
func TestLocalFSRefusesFlaggedEntries(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := NewLocalFS("Local", dir)
	withAttrs(t, map[string]attrSet{file: attrImmutable})

	wantAttrErr(t, l.Remove(file), file, "immutable")
	wantAttrErr(t, l.Rename(file, filepath.Join(dir, "g")), file, "immutable")
	wantAttrErr(t, l.Chmod(file, 0o600), file, "immutable")
	wantAttrErr(t, l.Chown(file, os.Getuid(), os.Getgid()), file, "immutable")

	if names := l.Attributes(file); len(names) != 1 || names[0] != "immutable" {
		t.Errorf("Attributes = %v, want [immutable]", names)
	}
	if names := l.Attributes(dir); len(names) != 0 {
		t.Errorf("Attributes of an unflagged dir = %v, want none", names)
	}

	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("the file must still be there: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode changed to %v despite the refusal", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, "g")); err == nil {
		t.Error("the rename must not have happened")
	}
}

// Something flagged deep inside a folder being deleted is only found when the
// removal fails: that failure must become the explanatory AttrError, not an
// elevated retry.
func TestLocalFSRemoveNestedFlaggedEntry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root the directory permissions used to provoke the failure don't apply")
	}
	root := t.TempDir()
	d := filepath.Join(root, "d")
	child := filepath.Join(d, "c")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A read-only directory makes the plain removal fail with a permission
	// error on the child, standing in for the kernel refusing to delete an
	// immutable one.
	if err := os.Chmod(d, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(d, 0o700) })
	withAttrs(t, map[string]attrSet{child: attrImmutable})

	wantAttrErr(t, NewLocalFS("Local", root).Remove(d), child, "immutable")
	if _, err := os.Stat(child); err != nil {
		t.Errorf("the flagged child must still exist: %v", err)
	}
}
