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

package fusemount

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"shfm/internal/vfs"
)

// unknownSizeFS is a dirFS whose files don't tell their size (as a Google
// Docs document exported on the fly): reported as zero, SizeUnknown.
type unknownSizeFS struct{ *dirFS }

func hideSize(e vfs.Entry) vfs.Entry {
	if !e.IsDir {
		e.Size, e.SizeUnknown = 0, true
	}
	return e
}

func (u unknownSizeFS) Stat(p string) (vfs.Entry, error) {
	e, err := u.dirFS.Stat(p)
	return hideSize(e), err
}

func (u unknownSizeFS) List(p string) ([]vfs.Entry, error) {
	entries, err := u.dirFS.List(p)
	for i := range entries {
		entries[i] = hideSize(entries[i])
	}
	return entries, err
}

func (u unknownSizeFS) Redial() (vfs.FileSystem, error) {
	be, err := u.dirFS.Redial()
	if err != nil {
		return nil, err
	}
	return unknownSizeFS{be.(*dirFS)}, nil
}

// TestReadUnknownSize: a file of unknown size is read whole, up to EOF,
// rather than stopping at its reported (zero) size.
func TestReadUnknownSize(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	backing := t.TempDir()
	data := bytes.Repeat([]byte("exported document "), 20000)
	if err := os.WriteFile(filepath.Join(backing, "doc.odt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	mg := NewManager(t.TempDir())
	t.Cleanup(mg.UnmountAll)
	local, err := mg.LocalPath(unknownSizeFS{newDirFS(backing)}, "/doc.odt")
	if err != nil {
		t.Skipf("FUSE mount not available here: %v", err)
	}
	got, err := os.ReadFile(local)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes (err %v), want %d", len(got), err, len(data))
	}
}
