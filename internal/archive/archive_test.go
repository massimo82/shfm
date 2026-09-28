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

package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestDetect(t *testing.T) {
	cases := map[string]struct {
		kind Kind
		base string
	}{
		"a.zip": {KindZip, "a"}, "dir/A.ZIP": {KindZip, "A"},
		"photos.tar.gz": {KindTarGz, "photos"}, "x.tgz": {KindTarGz, "x"}, "x.TAR.GZ": {KindTarGz, "x"},
		"x.tar.bz2": {KindTarBz2, "x"}, "x.tbz2": {KindTarBz2, "x"}, "x.tbz": {KindTarBz2, "x"},
		"x.tar.xz": {KindTarXz, "x"}, "x.txz": {KindTarXz, "x"},
		"x.tar.zst": {KindTarZstd, "x"}, "x.tzst": {KindTarZstd, "x"},
		"x.tar.lz": {KindTarLzip, "x"}, "x.tar.lz4": {KindTarLz4, "x"},
		"x.tar.lzma": {KindTarLzma, "x"}, "x.tlz": {KindTarLzma, "x"},
		"x.tar": {KindTar, "x"}, "report.txt.gz": {KindGzip, "report.txt"},
		"x.bz2": {KindBzip2, "x"}, "x.xz": {KindXz, "x"}, "x.lzma": {KindLzma, "x"},
		"x.zst": {KindZstd, "x"}, "x.lz": {KindLzip, "x"}, "x.lz4": {KindLz4, "x"},
		"x.7z": {KindSevenZip, "x"}, "x.rar": {KindRar, "x"},
		"x.txt": {"", "x.txt"}, ".zip": {"", ".zip"}, "": {"", ""},
	}
	for name, want := range cases {
		kind, base := Detect(name)
		if kind != want.kind || base != want.base {
			t.Errorf("Detect(%q) = %q, %q; want %q, %q", name, kind, base, want.kind, want.base)
		}
	}
}

func TestCleanName(t *testing.T) {
	ok := map[string]string{"a/b": "a/b", "/abs/x": "abs/x", "./a//b/": "a/b", "a/../b": "b", "./": "."}
	for raw, want := range ok {
		if got, err := cleanName(raw); err != nil || got != want {
			t.Errorf("cleanName(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"..", "../x", "a/../../x", "a\x00b"} {
		if _, err := cleanName(raw); err == nil {
			t.Errorf("cleanName(%q) accepted an unsafe name", raw)
		}
	}
}

func TestSafeSymlink(t *testing.T) {
	cases := []struct {
		name, target string
		want         bool
	}{
		{"a/l", "../b", true},
		{"a/b/l", "../../c", true},
		{"a/l", "b/c", true},
		{"l", "../x", false},
		{"a/l", "../../x", false},
		{"l", "/etc/passwd", false},
		{"l", "d/r/..", false}, // ".." after a component that may be a link
		{"l", "", false},
	}
	for _, c := range cases {
		if got := SafeSymlink(c.name, c.target); got != c.want {
			t.Errorf("SafeSymlink(%q, %q) = %v, want %v", c.name, c.target, got, c.want)
		}
	}
}

// fixture is what the round-trip tests put in and expect back.
var fixture = []struct {
	e       Entry
	content string
}{
	{Entry{Name: "root", Type: TypeDir, Mode: 0o755}, ""},
	{Entry{Name: "root/a.txt", Type: TypeFile, Mode: 0o644}, "alpha"},
	{Entry{Name: "root/sub", Type: TypeDir, Mode: 0o700}, ""},
	{Entry{Name: "root/sub/run.sh", Type: TypeFile, Mode: 0o755}, "#!/bin/sh\n"},
	{Entry{Name: "root/sub/empty", Type: TypeFile, Mode: 0o600}, ""},
	{Entry{Name: "root/link", Type: TypeSymlink, Linkname: "a.txt"}, ""},
}

func putFixture(put PutFunc) error {
	mod := time.Date(2024, 5, 6, 7, 8, 10, 0, time.UTC)
	for _, f := range fixture {
		e := f.e
		e.ModTime = mod
		e.Size = int64(len(f.content))
		if err := put(e, strings.NewReader(f.content)); err != nil {
			return err
		}
	}
	return nil
}

type walked struct {
	Type     EntryType
	Content  string
	Linkname string
}

func walkAll(t *testing.T, src Source) map[string]walked {
	t.Helper()
	got := map[string]walked{}
	err := Walk(context.Background(), src, func(e Entry, r io.Reader) error {
		if e.Skip != nil {
			t.Errorf("unexpected skip of %q: %v", e.Name, e.Skip)
			return nil
		}
		w := walked{Type: e.Type, Linkname: e.Linkname}
		if r != nil {
			b, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			w.Content = string(b)
		}
		got[e.Name] = w
		return nil
	})
	if err != nil {
		t.Fatalf("Walk(%s): %v", src.Name, err)
	}
	return got
}

// TestRoundTrip creates an archive in every format this machine can write
// and reads it back, both from a local path and through Open (as from a
// remote backend).
func TestRoundTrip(t *testing.T) {
	want := map[string]walked{}
	for _, f := range fixture {
		want[f.e.Name] = walked{Type: f.e.Type, Content: f.content, Linkname: f.e.Linkname}
	}
	for _, kind := range Creatable() {
		t.Run(string(kind), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "out"+kind.Ext())
			f, err := os.Create(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := Create(context.Background(), kind, f, t.TempDir(), putFixture); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if got := walkAll(t, Source{Name: p, LocalPath: p}); !reflect.DeepEqual(got, want) {
				t.Errorf("local read:\n got %v\nwant %v", got, want)
			}
			remote := Source{Name: filepath.Base(p), TempDir: t.TempDir(),
				Open: func() (io.ReadCloser, error) { return os.Open(p) }}
			if got := walkAll(t, remote); !reflect.DeepEqual(got, want) {
				t.Errorf("stream read:\n got %v\nwant %v", got, want)
			}
		})
	}
}

// withTools makes only the named tools visible to this package, for the
// duration of the test — without touching PATH.
func withTools(t *testing.T, visible ...string) {
	t.Helper()
	orig := lookPath
	lookPath = func(name string) (string, error) {
		for _, v := range visible {
			if v == name {
				return orig(name)
			}
		}
		return "", exec.ErrNotFound
	}
	resetTools()
	t.Cleanup(func() { lookPath = orig; resetTools() })
}

func TestNoTools(t *testing.T) {
	withTools(t)
	for _, k := range []Kind{KindZip, KindTar, KindTarGz, KindTarBz2, KindGzip, KindBzip2} {
		if !Available(k) {
			t.Errorf("Available(%s) = false without tools", k)
		}
	}
	for _, k := range []Kind{KindTarXz, KindXz, KindTarZstd, KindSevenZip, KindRar} {
		if Available(k) {
			t.Errorf("Available(%s) = true without tools", k)
		}
	}
	if got, want := Creatable(), []Kind{KindZip, KindTarGz, KindTar}; !reflect.DeepEqual(got, want) {
		t.Errorf("Creatable() = %v, want %v", got, want)
	}
	err := Walk(context.Background(), Source{Name: "x.7z", LocalPath: "/nonexistent"}, func(Entry, io.Reader) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "7-Zip") {
		t.Errorf("Walk of a .7z without tools: %v", err)
	}
}

// TestBsdtarFallback reads and writes .tar.xz and .7z through bsdtar alone.
func TestBsdtarFallback(t *testing.T) {
	if _, err := exec.LookPath("bsdtar"); err != nil {
		t.Skip("bsdtar not installed")
	}
	withTools(t, "bsdtar")
	want := map[string]walked{}
	for _, f := range fixture {
		want[f.e.Name] = walked{Type: f.e.Type, Content: f.content, Linkname: f.e.Linkname}
	}
	for _, kind := range []Kind{KindTarXz, KindTarZstd, KindSevenZip} {
		var buf bytes.Buffer
		if err := Create(context.Background(), kind, &buf, t.TempDir(), putFixture); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		p := filepath.Join(t.TempDir(), "out"+kind.Ext())
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := walkAll(t, Source{Name: p, LocalPath: p}); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", kind, got, want)
		}
	}
}

func TestUnsafeEntriesSkipped(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range []string{"../evil.txt", `..\win.txt`, "ok.txt"} {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("x"))
	}
	zw.Close()
	p := filepath.Join(t.TempDir(), "bad.zip")
	os.WriteFile(p, buf.Bytes(), 0o644)

	var ok, skipped []string
	err := Walk(context.Background(), Source{Name: p, LocalPath: p}, func(e Entry, r io.Reader) error {
		if e.Skip != nil {
			if !errors.Is(e.Skip, ErrUnsafePath) || r != nil {
				t.Errorf("%q: skip %v, reader %v", e.Name, e.Skip, r)
			}
			skipped = append(skipped, e.Name)
		} else {
			ok = append(ok, e.Name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(skipped)
	if !reflect.DeepEqual(ok, []string{"ok.txt"}) || len(skipped) != 2 {
		t.Errorf("ok %v, skipped %v", ok, skipped)
	}
}

func TestSingleGzipName(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.Name = "original.txt"
	gz.Write([]byte("content"))
	gz.Close()
	p := filepath.Join(t.TempDir(), "renamed.txt.gz")
	os.WriteFile(p, buf.Bytes(), 0o644)
	got := walkAll(t, Source{Name: p, LocalPath: p})
	if w, ok := got["original.txt"]; !ok || w.Content != "content" || len(got) != 1 {
		t.Errorf("got %v", got)
	}
}

func TestStop(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range []string{"a", "b", "c"} {
		tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Size: 1, Mode: 0o644})
		tw.Write([]byte("x"))
	}
	tw.Close()
	p := filepath.Join(t.TempDir(), "x.tar")
	os.WriteFile(p, buf.Bytes(), 0o644)
	n := 0
	err := Walk(context.Background(), Source{Name: p, LocalPath: p}, func(Entry, io.Reader) error {
		n++
		return ErrStop
	})
	if err != nil || n != 1 {
		t.Errorf("err %v after %d entries", err, n)
	}
}
