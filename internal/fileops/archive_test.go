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
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"shfm/internal/archive"
	"shfm/internal/vfs"
)

// tree lists every path under root (relative, '/'-separated), with a
// file's content, a symlink's "-> target", or "/" for a folder.
func tree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, _ := os.Readlink(p)
			out[rel] = "-> " + target
		case info.IsDir():
			out[rel] = "/"
		default:
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(t *testing.T, got, want map[string]string) {
	t.Helper()
	var keys []string
	for k := range got {
		keys = append(keys, k)
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if got[k] != want[k] {
			t.Errorf("%s: got %q, want %q", k, got[k], want[k])
		}
	}
}

// dumpArchive logs every entry of the archive at p, as shfm's reader and
// (when installed) bsdtar each see it, to tell a mode lost while creating
// the archive from one lost while reading it.
func dumpArchive(t *testing.T, p string) {
	t.Helper()
	err := archive.Walk(context.Background(), archive.Source{Name: p, LocalPath: p}, func(e archive.Entry, _ io.Reader) error {
		t.Logf("walk: %s type=%v mode=%v mtime=%v", e.Name, e.Type, e.Mode, e.ModTime)
		return nil
	})
	if err != nil {
		t.Logf("walk: %v", err)
	}
	if bsdtar, err := exec.LookPath("bsdtar"); err == nil {
		out, err := exec.Command(bsdtar, "-tvf", p).CombinedOutput()
		t.Logf("bsdtar -tvf: %v\n%s", err, out)
	}
}

func writeTarFile(t *testing.T, p string, hdrs []*tar.Header, contents []string) {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i, h := range hdrs {
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(contents[i]))
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(contents[i]))
		}
	}
	tw.Close()
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCreateThenExtract archives a folder and a file in every format this
// machine writes, then extracts each archive next to it: always one root
// folder named after the archive, holding the items.
func TestCreateThenExtract(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "docs", "deep"), 0o755)
	os.WriteFile(filepath.Join(src, "docs", "a.txt"), []byte("alpha"), 0o644)
	os.WriteFile(filepath.Join(src, "docs", "deep", "b.txt"), []byte("beta"), 0o600)
	os.WriteFile(filepath.Join(src, "note.md"), []byte("note"), 0o644)
	os.Symlink("a.txt", filepath.Join(src, "docs", "link"))
	// Whole seconds: the precision every format keeps.
	fileTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	dirTime := time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC)
	os.Chtimes(filepath.Join(src, "docs", "deep", "b.txt"), fileTime, fileTime)
	os.Chtimes(filepath.Join(src, "docs", "deep"), dirTime, dirTime)
	fs := vfs.NewLocalFS("local", "/")
	items := []Item{{FS: fs, Path: filepath.Join(src, "docs")}, {FS: fs, Path: filepath.Join(src, "note.md")}}

	for _, kind := range archive.Creatable() {
		t.Run(string(kind), func(t *testing.T) {
			out := t.TempDir()
			dest := filepath.Join(out, "bundle"+kind.Ext())
			if res := CreateArchive(items, fs, dest, kind, nil); len(res.Errors) > 0 || res.Done != 2 {
				t.Fatalf("CreateArchive: %+v", res)
			}
			res := Extract([]Item{{FS: fs, Path: dest}}, nil)
			if len(res.Errors) > 0 || res.Done != 1 {
				t.Fatalf("Extract: %+v", res)
			}
			sameTree(t, tree(t, filepath.Join(out, "bundle")), map[string]string{
				"bundle":                 "/",
				"bundle/docs":            "/",
				"bundle/docs/a.txt":      "alpha",
				"bundle/docs/deep":       "/",
				"bundle/docs/deep/b.txt": "beta",
				"bundle/docs/link":       "-> a.txt",
				"bundle/note.md":         "note",
			})
			deep := filepath.Join(out, "bundle", "bundle", "docs", "deep")
			st, err := os.Stat(filepath.Join(deep, "b.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != 0o600 {
				t.Errorf("mode of b.txt: %v", st.Mode())
				dumpArchive(t, dest)
			}
			if !st.ModTime().Equal(fileTime) {
				t.Errorf("mtime of b.txt: %v, want %v", st.ModTime(), fileTime)
			}
			if st, err := os.Stat(deep); err != nil || !st.ModTime().Equal(dirTime) {
				t.Errorf("mtime of docs/deep: %v, want %v", st, dirTime)
			}
		})
	}
}

func TestExtractFolderNameAndCollision(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Photos.2024.tar")
	writeTarFile(t, p, []*tar.Header{{Name: "x.txt", Typeflag: tar.TypeReg}}, []string{"x"})
	os.Mkdir(filepath.Join(dir, "Photos.2024"), 0o755)
	fs := vfs.NewLocalFS("local", "/")
	if res := Extract([]Item{{FS: fs, Path: p}}, nil); len(res.Errors) > 0 {
		t.Fatal(res.Error())
	}
	if b, err := os.ReadFile(filepath.Join(dir, "Photos.2024 (2)", "x.txt")); err != nil || string(b) != "x" {
		t.Errorf("x.txt: %q, %v", b, err)
	}
}

// TestExtractUnsafe checks that nothing is ever written outside the new
// folder: "../" names, absolute names, symlinks pointing out, and writing
// through a symlink are all refused, and reported.
func TestExtractUnsafe(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "evil.tar")
	writeTarFile(t, p, []*tar.Header{
		{Name: "../escape.txt", Typeflag: tar.TypeReg},
		{Name: "/abs.txt", Typeflag: tar.TypeReg},
		{Name: "out", Typeflag: tar.TypeSymlink, Linkname: "../"},
		{Name: "etc", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
		{Name: "d/up", Typeflag: tar.TypeSymlink, Linkname: ".."},
		{Name: "d/up/through", Typeflag: tar.TypeSymlink, Linkname: "x"},
		{Name: "trick", Typeflag: tar.TypeSymlink, Linkname: "d/up/.."},
		{Name: "ok.txt", Typeflag: tar.TypeReg},
		{Name: "dev", Typeflag: tar.TypeFifo},
	}, []string{"e", "a", "", "", "", "", "", "ok", ""})
	fs := vfs.NewLocalFS("local", "/")
	res := Extract([]Item{{FS: fs, Path: p}}, nil)
	if len(res.Errors) != 1 || !strings.Contains(res.Error(), "6 entries skipped") {
		t.Errorf("errors: %v", res.Error())
	}
	if _, err := os.Lstat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Error("escape.txt was written outside the folder")
	}
	sameTree(t, tree(t, filepath.Join(dir, "evil")), map[string]string{
		"abs.txt": "a",
		"ok.txt":  "ok",
		"d":       "/",
		"d/up":    "-> ..",
	})
}

// TestExtractRemote extracts through a backend that isn't local (no
// LocalPath, no symlinks) and must know sizes up front, like MTP.
func TestExtractRemote(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.tar.gz")
	// Built on the local FS, then read "remotely".
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("remote"), 0o644)
	os.Symlink("f.txt", filepath.Join(dir, "l"))
	local := vfs.NewLocalFS("local", "/")
	res := CreateArchive([]Item{{FS: local, Path: filepath.Join(dir, "f.txt")}, {FS: local, Path: filepath.Join(dir, "l")}}, local, p, archive.KindTarGz, nil)
	if len(res.Errors) > 0 {
		t.Fatal(res.Error())
	}

	remote := &sizedFS{remoteFS: remoteFS{local}}
	res = Extract([]Item{{FS: remote, Path: p}}, nil)
	if len(res.Errors) != 1 || !strings.Contains(res.Error(), "symbolic links aren't supported") {
		t.Errorf("errors: %v", res.Error())
	}
	if b, err := os.ReadFile(filepath.Join(dir, "r", "r", "f.txt")); err != nil || string(b) != "remote" {
		t.Errorf("f.txt: %q, %v", b, err)
	}
	if remote.sized == 0 {
		t.Error("CreateSized never used")
	}

	// And archiving from/to it.
	dest := filepath.Join(dir, "again.zip")
	res = CreateArchive([]Item{{FS: remote, Path: filepath.Join(dir, "r")}}, remote, dest, archive.KindZip, nil)
	if len(res.Errors) > 0 {
		t.Fatal(res.Error())
	}
	res = Extract([]Item{{FS: local, Path: dest}}, nil)
	if len(res.Errors) > 0 {
		t.Fatal(res.Error())
	}
	if b, err := os.ReadFile(filepath.Join(dir, "again", "again", "r", "r", "f.txt")); err != nil || string(b) != "remote" {
		t.Errorf("again f.txt: %q, %v", b, err)
	}
}

func TestExtractCancelled(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.tar")
	writeTarFile(t, p, []*tar.Header{{Name: "a", Typeflag: tar.TypeReg}, {Name: "b", Typeflag: tar.TypeReg}}, []string{"1", "2"})
	fs := vfs.NewLocalFS("local", "/")
	n := 0
	prog := &Progress{Cancelled: func() bool { n++; return n > 2 }}
	res := Extract([]Item{{FS: fs, Path: p}}, prog)
	if !res.Cancelled {
		t.Errorf("not cancelled: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "c")); !os.IsNotExist(err) {
		t.Errorf("partial folder left behind: %v", err)
	}
}

func TestCreateArchiveCancelledRemovesFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	fs := vfs.NewLocalFS("local", "/")
	dest := filepath.Join(dir, "out.zip")
	prog := &Progress{Cancelled: func() bool { return true }}
	res := CreateArchive([]Item{{FS: fs, Path: filepath.Join(dir, "f")}}, fs, dest, archive.KindZip, prog)
	if !res.Cancelled {
		t.Errorf("not cancelled: %+v", res)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("incomplete archive left behind: %v", err)
	}
}

// remoteFS hides LocalFS's local-only extras (LocalPath, TimesSetter...),
// like a network backend.
type remoteFS struct{ fs vfs.FileSystem }

func (r remoteFS) Kind() vfs.Kind                          { return vfs.KindSFTP }
func (r remoteFS) Label() string                           { return "remote" }
func (r remoteFS) Root() string                            { return "/" }
func (r remoteFS) List(p string) ([]vfs.Entry, error)      { return r.fs.List(p) }
func (r remoteFS) Stat(p string) (vfs.Entry, error)        { return r.fs.Stat(p) }
func (r remoteFS) Mkdir(p string) error                    { return r.fs.Mkdir(p) }
func (r remoteFS) CreateEmptyFile(p string) error          { return r.fs.CreateEmptyFile(p) }
func (r remoteFS) Remove(p string) error                   { return r.fs.Remove(p) }
func (r remoteFS) Rename(a, b string) error                { return r.fs.Rename(a, b) }
func (r remoteFS) Open(p string) (io.ReadCloser, error)    { return r.fs.Open(p) }
func (r remoteFS) Create(p string) (io.WriteCloser, error) { return r.fs.Create(p) }
func (r remoteFS) Join(e ...string) string                 { return r.fs.Join(e...) }
func (r remoteFS) Dir(p string) string                     { return r.fs.Dir(p) }
func (r remoteFS) Base(p string) string                    { return r.fs.Base(p) }
func (r remoteFS) SupportsTrash() bool                     { return false }
func (r remoteFS) Close() error                            { return nil }

// sizedFS is a remoteFS that, like MTP, wants every file's size up front.
type sizedFS struct {
	remoteFS
	sized int
}

func (s *sizedFS) CreateSized(p string, size int64) (io.WriteCloser, error) {
	s.sized++
	return &exactWriter{path: p, left: size}, nil
}

// exactWriter fails unless exactly the declared size is written.
type exactWriter struct {
	path string
	left int64
	buf  bytes.Buffer
}

func (w *exactWriter) Write(p []byte) (int, error) {
	w.left -= int64(len(p))
	return w.buf.Write(p)
}

func (w *exactWriter) Close() error {
	if w.left != 0 {
		return io.ErrShortWrite
	}
	return os.WriteFile(w.path, w.buf.Bytes(), 0o644)
}
