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

package ui

import (
	"slices"
	"testing"

	"shfm/internal/config"
	"shfm/internal/vfs"
)

type labelFS struct {
	vfs.FileSystem
	kind  vfs.Kind
	label string
}

func (l labelFS) Kind() vfs.Kind { return l.kind }
func (l labelFS) Label() string  { return l.label }

func TestFileURIRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		fs   vfs.FileSystem
		path string
		want string
	}{
		{vfs.NewLocalFS("Local", "/"), "/home/u/My Docs/a#1.txt", "file:///home/u/My%20Docs/a%231.txt"},
		{labelFS{kind: vfs.KindSMB, label: "smb://nas/share"}, "/dir/x y.pdf", "smb://nas/share/dir/x%20y.pdf"},
		{labelFS{kind: vfs.KindSFTP, label: "sftp://me@host"}, "/srv/f", "sftp://me@host/srv/f"},
		{labelFS{kind: vfs.KindMTP, label: "mtp://Google Pixel 7 1234"}, "/DCIM/a.jpg", "mtp://Google%20Pixel%207%201234/DCIM/a.jpg"},
	} {
		u := fileURI(tc.fs, tc.path)
		if u != tc.want {
			t.Errorf("fileURI(%q) = %q, want %q", tc.path, u, tc.want)
			continue
		}
		scheme, rest, ok := decodeURI(u)
		if !ok {
			t.Fatalf("decodeURI(%q) failed", u)
		}
		if tc.fs.Kind() == vfs.KindLocal {
			if scheme != "file" || rest != tc.path {
				t.Errorf("decode %q = %q %q", u, scheme, rest)
			}
			continue
		}
		if p, ok := pathUnder(tc.fs.Label(), scheme, rest); !ok || p != tc.path {
			t.Errorf("pathUnder(%q) = %q, %v; want %q", u, p, ok, tc.path)
		}
	}
}

func TestPathUnderIgnoresPortAndSiblings(t *testing.T) {
	if p, ok := pathUnder("sftp://me@host", "sftp", "me@host:22/srv/f"); !ok || p != "/srv/f" {
		t.Errorf("port: got %q, %v", p, ok)
	}
	if _, ok := pathUnder("smb://nas/share", "smb", "nas/shared/x"); ok {
		t.Error("matched a sibling share with a common prefix")
	}
	if p, ok := pathUnder("smb://nas/share", "smb", "nas/share"); !ok || p != "/" {
		t.Errorf("root: got %q, %v", p, ok)
	}
}

func TestParseURIList(t *testing.T) {
	got := parseURIList([]byte("# comment\r\nfile:///a\r\n\r\nsmb://h/s/b\r\n"))
	if !slices.Equal(got, []string{"file:///a", "smb://h/s/b"}) {
		t.Errorf("uri-list: %q", got)
	}
	got = parseURIList([]byte("copy\nfile:///a\nfile:///b"))
	if !slices.Equal(got, []string{"file:///a", "file:///b"}) {
		t.Errorf("gnome-copied-files: %q", got)
	}
}

func TestResolveRemoteURIs(t *testing.T) {
	m, _ := mirrorTestModel(t)
	sftp := labelFS{FileSystem: vfs.NewLocalFS("x", "/"), kind: vfs.KindSFTP, label: "sftp://me@host"}
	m.panes[1] = &Pane{FS: sftp, Path: "/"}
	m.cfg.RemoteSources = []config.RemoteSource{{Kind: "smb", Host: "nas", Share: "docs"}}
	local := vfs.NewLocalFS("Local", "/")

	src, path, err := m.resolveURI("sftp://me@host:22/srv/a%20b.txt", local)
	if err != nil || src.fs != sftp || path != "/srv/a b.txt" {
		t.Fatalf("open pane: %+v %q %v", src, path, err)
	}
	src, path, err = m.resolveURI("smb://nas/docs/x/y.pdf", local)
	if err != nil || src.fs != nil || src.dial == nil || src.label != "smb://nas/docs" || path != "/x/y.pdf" {
		t.Fatalf("saved source: %+v %q %v", src, path, err)
	}
	src, path, err = m.resolveURI("file:///tmp/z", local)
	if err != nil || src.fs != local || path != "/tmp/z" {
		t.Fatalf("local: %+v %q %v", src, path, err)
	}
	if _, _, err := m.resolveURI("smb://other/share/f", local); err == nil {
		t.Fatal("expected an error for an unknown source")
	}
}
