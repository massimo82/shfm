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

package fileops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"shfm/internal/archive"
	"shfm/internal/vfs"
)

// unknownSizeFS is a remoteFS whose files don't tell their size (as a
// Google Docs document exported on the fly): reported as zero,
// SizeUnknown.
type unknownSizeFS struct{ remoteFS }

func hideSize(e vfs.Entry) vfs.Entry {
	if !e.IsDir {
		e.Size, e.SizeUnknown = 0, true
	}
	return e
}

func (u unknownSizeFS) Stat(p string) (vfs.Entry, error) {
	e, err := u.remoteFS.Stat(p)
	return hideSize(e), err
}

func (u unknownSizeFS) List(p string) ([]vfs.Entry, error) {
	entries, err := u.remoteFS.List(p)
	for i := range entries {
		entries[i] = hideSize(entries[i])
	}
	return entries, err
}

// TestCopyUnknownSize: a file of unknown size is copied whole, even to a
// backend that wants sizes up front, and archived whole.
func TestCopyUnknownSize(t *testing.T) {
	dir := t.TempDir()
	content := strings.Repeat("document ", 1000)
	if err := os.WriteFile(filepath.Join(dir, "doc.odt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	local := vfs.NewLocalFS("local", "/")
	src := unknownSizeFS{remoteFS{local}}

	dst := &sizedFS{remoteFS: remoteFS{local}}
	if err := CopyTo(src, filepath.Join(dir, "doc.odt"), dst, filepath.Join(dir, "copy.odt")); err != nil {
		t.Fatalf("CopyTo: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "copy.odt")); string(got) != content {
		t.Errorf("copy has %d bytes, want %d", len(got), len(content))
	}
	if dst.sized != 0 {
		t.Error("CreateSized was told a size the source doesn't know")
	}

	arch := filepath.Join(dir, "docs.tar")
	res := CreateArchive([]Item{{FS: src, Path: filepath.Join(dir, "doc.odt")}}, local, arch, archive.KindTar, nil)
	if len(res.Errors) > 0 {
		t.Fatal(res.Error())
	}
	res = Extract([]Item{{FS: local, Path: arch}}, nil)
	if len(res.Errors) > 0 {
		t.Fatal(res.Error())
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "docs", "*doc.odt"))
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(dir, "docs", "*", "doc.odt"))
	}
	if len(matches) != 1 {
		t.Fatalf("extracted: %v", matches)
	}
	if got, _ := os.ReadFile(matches[0]); string(got) != content {
		t.Errorf("archived copy has %d bytes, want %d", len(got), len(content))
	}
}
