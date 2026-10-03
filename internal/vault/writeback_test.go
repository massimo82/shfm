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

//go:build vault

package vault

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"shfm/internal/fusemount"
	"shfm/internal/vfs"
)

// spoolIn points the working copies at a test folder.
func spoolIn(t *testing.T) string {
	dir := t.TempDir()
	prev := SpoolDir
	SpoolDir = func() (string, bool) { return dir, true }
	t.Cleanup(func() { SpoolDir = prev })
	return dir
}

func TestWriteBack(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		spool := spoolIn(t)
		_, _, _, fs := newVault(t, opts)
		ra := fs.(vfs.RandomAccessOpener)
		writeFile(t, fs, "/doc.txt", []byte("hello world"))

		// Edit in place: the rest of the content is kept.
		f, err := ra.OpenRandom("/doc.txt", os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt([]byte("HELLO"), 0); err != nil {
			t.Fatal(err)
		}
		if entries, _ := os.ReadDir(spool); len(entries) != 0 {
			t.Fatalf("the decrypted working copy is visible in the spool folder: %v", entries)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, fs, "/doc.txt"); string(got) != "HELLO world" {
			t.Fatalf("after the edit: %q", got)
		}

		// Created by open(2), there even before the first write.
		f, err = ra.OpenRandom("/new.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fs.Stat("/new.txt"); err != nil {
			t.Fatalf("not created on open: %v", err)
		}
		f.WriteAt([]byte("fresh"), 0)
		f.Close()
		if got := readFile(t, fs, "/new.txt"); string(got) != "fresh" {
			t.Fatalf("new file: %q", got)
		}
		if _, err := ra.OpenRandom("/new.txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0); !errors.Is(err, os.ErrExist) {
			t.Fatalf("O_EXCL on an existing file: %v", err)
		}

		// Truncated on open, then rewritten shorter.
		f, _ = ra.OpenRandom("/doc.txt", os.O_WRONLY|os.O_TRUNC, 0)
		f.WriteAt([]byte("x"), 0)
		f.Close()
		if got := readFile(t, fs, "/doc.txt"); string(got) != "x" {
			t.Fatalf("after O_TRUNC: %q", got)
		}

		// Opened for writing but unchanged: not rewritten.
		before, _ := fs.Stat("/new.txt")
		f, _ = ra.OpenRandom("/new.txt", os.O_RDWR, 0)
		f.Close()
		if after, _ := fs.Stat("/new.txt"); !after.ModTime.Equal(before.ModTime) {
			t.Fatal("an unchanged file was rewritten")
		}
	})
}

// Without a tmpfs for the working copy, writing is refused rather than
// done through clear text on a disk.
func TestWriteBackNeedsRuntimeDir(t *testing.T) {
	prev := SpoolDir
	SpoolDir = func() (string, bool) { return "", false }
	t.Cleanup(func() { SpoolDir = prev })
	_, _, _, fs := newVault(t, Options{})
	writeFile(t, fs, "/a", []byte("x"))
	if _, err := fs.(vfs.RandomAccessOpener).OpenRandom("/a", os.O_RDWR, 0); !errors.Is(err, syscall.EROFS) {
		t.Fatalf("write without XDG_RUNTIME_DIR: %v, want EROFS", err)
	}
}

// A vault on the local disk mounts through FUSE, for external
// applications, read and write; its mount is gone once unmounted.
func TestFUSEMount(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	spoolIn(t)
	_, _, _, fs := newVault(t, Options{})
	writeFile(t, fs, "/inside.txt", []byte("decrypted by FUSE"))
	if !fusemount.Supported(fs) {
		t.Fatal("a vault on the local disk isn't mountable")
	}
	mg := fusemount.NewManager(t.TempDir())
	t.Cleanup(mg.Close)
	local, err := mg.LocalPath(fs, "/inside.txt")
	if err != nil {
		t.Skipf("FUSE mount not available here: %v", err)
	}
	got, err := os.ReadFile(local)
	if err != nil || string(got) != "decrypted by FUSE" {
		t.Fatalf("read through FUSE: %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(local), "saved.txt"), []byte("saved by an app"), 0o644); err != nil {
		t.Fatalf("write through FUSE: %v", err)
	}
	if got := readFile(t, fs, "/saved.txt"); string(got) != "saved by an app" {
		t.Fatalf("saved through FUSE: %q", got)
	}
	mg.Unmount(fs)
	if _, err := os.Stat(local); err == nil {
		t.Fatal("still reachable after Unmount")
	}
}
