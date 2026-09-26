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

package mirror

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"shfm/internal/vfs"
)

// shareFS stands for a network share of a local folder: its paths are
// relative to root, and it reaches the very same files.
type shareFS struct {
	vfs.FileSystem
	root string
}

func (s shareFS) Kind() vfs.Kind { return vfs.KindSMB }
func (s shareFS) Label() string  { return "smb://host/share" }
func (s shareFS) at(p string) string {
	return filepath.Join(s.root, p)
}
func (s shareFS) List(p string) ([]vfs.Entry, error) { return s.FileSystem.List(s.at(p)) }
func (s shareFS) Stat(p string) (vfs.Entry, error)   { return s.FileSystem.Stat(s.at(p)) }
func (s shareFS) CreateEmptyFile(p string) error     { return s.FileSystem.CreateEmptyFile(s.at(p)) }
func (s shareFS) Remove(p string) error              { return s.FileSystem.Remove(s.at(p)) }
func (s shareFS) Join(elem ...string) string         { return filepath.Join(elem...) }
func (s shareFS) Dir(p string) string                { return filepath.Dir(p) }
func (s shareFS) Base(p string) string               { return filepath.Base(p) }

func overlapTree(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	for _, d := range []string{"home/docs/deep/er", "home/other", "elsewhere"} {
		os.MkdirAll(filepath.Join(tmp, d), 0o755)
	}
	os.WriteFile(filepath.Join(tmp, "home/docs/a.txt"), []byte("a"), 0o644)
	return tmp
}

func TestOverlapThroughShare(t *testing.T) {
	tmp := overlapTree(t)
	local := vfs.NewLocalFS("Local", "/")
	share := shareFS{FileSystem: local, root: filepath.Join(tmp, "home")}
	src := Side{FS: local, Path: filepath.Join(tmp, "home", "docs")}
	for _, tc := range []struct {
		dst  string // on the share
		want error
	}{
		{"/docs", ErrOverlap},              // the source itself
		{"/docs/deep/er/copy", ErrOverlap}, // inside the source
		{"/", ErrOverlap},                  // contains the source
		{"/other/docs", nil},
		{"/docs-copy", nil}, // next to the source, not it
	} {
		got := Overlap(src, Side{FS: share, Path: tc.dst}, nil)
		if !errors.Is(got, tc.want) && got != tc.want {
			t.Errorf("dst %s: got %v, want %v", tc.dst, got, tc.want)
		}
		// Same answer the other way round, and no probe left behind.
		back := Overlap(Side{FS: share, Path: tc.dst}, src, nil)
		if !errors.Is(back, tc.want) && back != tc.want {
			t.Errorf("dst %s as source: got %v, want %v", tc.dst, back, tc.want)
		}
	}
	filepath.Walk(tmp, func(p string, _ os.FileInfo, _ error) error {
		if filepath.Base(p) != "." && len(filepath.Base(p)) > 19 && filepath.Base(p)[:19] == ".shfm-mirror-probe-" {
			t.Errorf("probe left behind: %s", p)
		}
		return nil
	})
}

func TestOverlapLocal(t *testing.T) {
	tmp := overlapTree(t)
	os.Symlink(filepath.Join(tmp, "home"), filepath.Join(tmp, "link"))
	local := vfs.NewLocalFS("Local", "/")
	src := Side{FS: local, Path: filepath.Join(tmp, "home", "docs")}
	for _, tc := range []struct {
		dst  string
		want error
	}{
		{"link/docs", ErrOverlap},
		{"link/docs/new/copy", ErrOverlap},
		{"link", ErrOverlap},
		{"link/docs-copy", nil},
		{"elsewhere/docs", nil},
	} {
		got := Overlap(src, Side{FS: local, Path: filepath.Join(tmp, tc.dst)}, nil)
		if got != tc.want {
			t.Errorf("dst %s: got %v, want %v", tc.dst, got, tc.want)
		}
	}
}

// A folder that was only copied holds the same content as the source but
// is other files: mirroring onto it is allowed.
func TestOverlapAllowsCopiedFolder(t *testing.T) {
	tmp := overlapTree(t)
	local := vfs.NewLocalFS("Local", "/")
	copyDir := filepath.Join(tmp, "elsewhere", "docs")
	os.MkdirAll(filepath.Join(copyDir, "deep", "er"), 0o755)
	os.WriteFile(filepath.Join(copyDir, "a.txt"), []byte("a"), 0o644)
	src := Side{FS: local, Path: filepath.Join(tmp, "home", "docs")}
	if err := Overlap(src, Side{FS: local, Path: copyDir}, nil); err != nil {
		t.Fatalf("local copy: %v", err)
	}
	share := shareFS{FileSystem: local, root: filepath.Join(tmp, "elsewhere")}
	if err := Overlap(src, Side{FS: share, Path: "/docs"}, nil); err != nil {
		t.Fatalf("copy on a share: %v", err)
	}
}

func TestOverlapUnknownWhenTooBigToSearch(t *testing.T) {
	tmp := overlapTree(t)
	local := vfs.NewLocalFS("Local", "/")
	share := shareFS{FileSystem: local, root: filepath.Join(tmp, "elsewhere")}
	old := probeMaxDirs
	probeMaxDirs = 1
	defer func() { probeMaxDirs = old }()
	got := Overlap(Side{FS: local, Path: filepath.Join(tmp, "home")}, Side{FS: share, Path: "/copy"}, nil)
	if got != ErrOverlapUnknown {
		t.Fatalf("got %v, want ErrOverlapUnknown", got)
	}
}

func TestOverlapCancelled(t *testing.T) {
	tmp := overlapTree(t)
	local := vfs.NewLocalFS("Local", "/")
	share := shareFS{FileSystem: local, root: filepath.Join(tmp, "elsewhere")}
	got := Overlap(Side{FS: local, Path: filepath.Join(tmp, "home")}, Side{FS: share, Path: "/copy"}, func() bool { return true })
	if got != ErrOverlapUnknown {
		t.Fatalf("got %v, want ErrOverlapUnknown", got)
	}
}

func TestRunRefusesOverlap(t *testing.T) {
	tmp := overlapTree(t)
	local := vfs.NewLocalFS("Local", "/")
	share := shareFS{FileSystem: local, root: filepath.Join(tmp, "home")}
	res := Run(Side{FS: local, Path: filepath.Join(tmp, "home", "docs")}, Side{FS: share, Path: "/docs"}, Options{}, nil)
	if len(res.Errors) != 1 || !errors.Is(res.Errors[0], ErrOverlap) {
		t.Fatalf("errors = %v", res.Errors)
	}
	if _, err := os.Stat(filepath.Join(tmp, "home", "docs", "a.txt")); err != nil {
		t.Fatalf("source touched: %v", err)
	}
}
