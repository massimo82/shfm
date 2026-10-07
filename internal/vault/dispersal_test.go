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
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"shfm/internal/vfs"
)

// newDispersed returns a DispersedFS over three new local folders.
func newDispersed(t *testing.T) (*DispersedFS, [3]string) {
	t.Helper()
	var dirs [3]string
	var parts [3]Part
	for i := range parts {
		dirs[i] = t.TempDir()
		parts[i] = Part{FS: vfs.NewLocalFS("part", "/"), Dir: dirs[i]}
	}
	return NewDispersed("split", parts), dirs
}

// without returns the same parts with part i unreachable.
func without(d *DispersedFS, i int) *DispersedFS {
	parts := d.Parts()
	parts[i].FS = nil
	return NewDispersed("split", parts)
}

// shardsOf lists the shard files of name in dir.
func shardsOf(t *testing.T, dir, name string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, name+".*"))
	sort.Strings(m)
	return m
}

func TestDispersedRoundTrip(t *testing.T) {
	d, dirs := newDispersed(t)
	for _, n := range []int{0, 1, 2, 3, 1000, 65537} {
		data := randBytes(n)
		writeFile(t, d, "/f", data)
		if got := readFile(t, d, "/f"); !bytes.Equal(got, data) {
			t.Fatalf("%d bytes: read back differs", n)
		}
		e, err := d.Stat("/f")
		if err != nil || e.Size != int64(n) {
			t.Fatalf("%d bytes: Stat %+v %v", n, e, err)
		}
		// One generation only: the previous write's shards are gone.
		for i := range dirs {
			if s := shardsOf(t, dirs[i], "f"); len(s) != 1 {
				t.Fatalf("part %d holds %v", i, s)
			}
		}
		// Every part, short of the whole: in any degraded pair the file
		// is still read whole.
		for i := range 3 {
			if got := readFile(t, without(d, i), "/f"); !bytes.Equal(got, data) {
				t.Fatalf("%d bytes without part %d: differs", n, i)
			}
		}
	}
}

// No part holds the file: A and B are halves, C is noise.
func TestDispersedNoPartHasTheFile(t *testing.T) {
	d, dirs := newDispersed(t)
	data := []byte(strings.Repeat("secret!", 100))
	writeFile(t, d, "/s", data)
	for i := range dirs {
		raw, _ := os.ReadFile(shardsOf(t, dirs[i], "s")[0])
		if len(raw) > len(data)/2+1 || bytes.Contains(raw, data) {
			t.Fatalf("part %d holds %d bytes", i, len(raw))
		}
	}
}

// The recovery RECOVERY.txt describes: cat A B, or XOR from C.
func TestDispersedManualRecovery(t *testing.T) {
	d, dirs := newDispersed(t)
	data := randBytes(1001)
	writeFile(t, d, "/f", data)
	a, _ := os.ReadFile(shardsOf(t, dirs[0], "f")[0])
	b, _ := os.ReadFile(shardsOf(t, dirs[1], "f")[0])
	cName := shardsOf(t, dirs[2], "f")[0]
	c, _ := os.ReadFile(cName)
	if !bytes.Equal(append(append([]byte{}, a...), b...), data) {
		t.Fatal("cat A B isn't the file")
	}
	if !strings.HasSuffix(cName, ".c1") {
		t.Fatalf("odd size, C named %s", cName)
	}
	rebuiltB := make([]byte, len(a))
	for i := range a {
		rebuiltB[i] = a[i] ^ c[i]
	}
	if !bytes.Equal(rebuiltB[:len(a)-1], b) {
		t.Fatal("A XOR C isn't B")
	}
}

func TestDispersedFoldersRenameRemove(t *testing.T) {
	d, dirs := newDispersed(t)
	if err := d.Mkdir("/docs"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, d, "/docs/a", []byte("A"))
	if err := d.Rename("/docs/a", "/docs/b"); err != nil {
		t.Fatal(err)
	}
	if err := d.Rename("/docs", "/papers"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, d, "/papers/b"); string(got) != "A" {
		t.Fatalf("after renames: %q", got)
	}
	if got := names(t, d, "/"); strings.Join(got, ",") != "papers/" {
		t.Fatalf("root: %v", got)
	}
	if err := d.Remove("/papers"); err != nil {
		t.Fatal(err)
	}
	for i := range dirs {
		if entries, _ := os.ReadDir(dirs[i]); len(entries) != 0 {
			t.Fatalf("part %d not empty: %v", i, entries)
		}
	}
}

// A change needs every part; with one missing, the content stays readable.
func TestDispersedDegradedIsReadOnly(t *testing.T) {
	d, _ := newDispersed(t)
	writeFile(t, d, "/f", []byte("x"))
	deg := without(d, 1)
	if _, err := deg.Create("/g"); !errors.Is(err, ErrPartUnavailable) {
		t.Fatalf("Create degraded: %v", err)
	}
	if err := deg.Mkdir("/d"); !errors.Is(err, ErrPartUnavailable) {
		t.Fatalf("Mkdir degraded: %v", err)
	}
	if err := deg.Remove("/f"); !errors.Is(err, ErrPartUnavailable) {
		t.Fatalf("Remove degraded: %v", err)
	}
	two := without(without(d, 0), 1)
	if _, err := two.List("/"); err == nil {
		t.Fatal("listed with one part")
	}
}

// A write that reached only one part is ignored: the reader puts together
// the two shards of the same, older generation.
func TestDispersedInterruptedWrite(t *testing.T) {
	d, dirs := newDispersed(t)
	writeFile(t, d, "/f", []byte("old content"))
	stray := filepath.Join(dirs[0], "f.0badbeef.a")
	os.WriteFile(stray, []byte("new co"), 0o644)
	d.invalidate("/")
	if got := readFile(t, d, "/f"); string(got) != "old content" {
		t.Fatalf("mixed generations: %q", got)
	}
}

func TestDispersedRepair(t *testing.T) {
	d, dirs := newDispersed(t)
	d.Mkdir("/sub")
	data := randBytes(70000)
	writeFile(t, d, "/sub/f", data)
	writeFile(t, d, "/g", []byte("g"))

	// Part 2 lost, replaced by an empty folder.
	os.RemoveAll(dirs[1])
	os.Mkdir(dirs[1], 0o755)
	// A leftover of an older write on part 3.
	os.WriteFile(filepath.Join(dirs[2], "g.0badbeef.c0"), []byte("zz"), 0o644)

	st, err := RepairDispersed(d)
	if err != nil {
		t.Fatal(err)
	}
	if st.Folders != 1 || st.Shards != 2 || st.Removed != 1 || st.Lost != 0 {
		t.Fatalf("repair: %+v", st)
	}
	// Now part 1 can go: B was rebuilt.
	if got := readFile(t, without(d, 0), "/sub/f"); !bytes.Equal(got, data) {
		t.Fatal("rebuilt shards don't give the file back")
	}
	if s := shardsOf(t, dirs[2], "g"); len(s) != 1 {
		t.Fatalf("stale shard left: %v", s)
	}
}

func TestDispersedRandomAccess(t *testing.T) {
	d, _ := newDispersed(t)
	data := randBytes(3001)
	writeFile(t, d, "/f", data)
	for i := -1; i < 3; i++ {
		fs := d
		if i >= 0 {
			fs = without(d, i)
		}
		f, err := fs.OpenRandom("/f", os.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, off := range []int64{0, 1, 1499, 1500, 1501, 2990} {
			buf := make([]byte, 20)
			n, err := f.ReadAt(buf, off)
			want := data[off:min(off+20, int64(len(data)))]
			if (err != nil && err != io.EOF) || !bytes.Equal(buf[:n], want) {
				t.Fatalf("without part %d, ReadAt(%d): %d bytes, %v", i, off, n, err)
			}
		}
		f.Close()
	}
}

// A whole vault on three parts: written with all of them, read with any
// two, and no part alone holds an age file.
func TestSplitVault(t *testing.T) {
	d, dirs := newDispersed(t)
	key, err := Create(d, "/", pw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	v, err := Unlock(d, "/", pw)
	if err != nil {
		t.Fatal(err)
	}
	fs := v.FS()
	fs.Mkdir("/docs")
	data := randBytes(3*chunkSize + 11)
	writeFile(t, fs, "/docs/report.pdf", data)

	for i := range 3 {
		deg := without(d, i)
		v2, err := UnlockWithKey(deg, "/", key)
		if err != nil {
			t.Fatalf("without part %d: %v", i, err)
		}
		if got := readFile(t, v2.FS(), "/docs/report.pdf"); !bytes.Equal(got, data) {
			t.Fatalf("without part %d: differs", i)
		}
		ra := v2.FS().(vfs.RandomAccessOpener)
		f, err := ra.OpenRandom("/docs/report.pdf", os.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("random access without part %d: %v", i, err)
		}
		buf := make([]byte, 100)
		if n, err := f.ReadAt(buf, chunkSize+5); err != nil || !bytes.Equal(buf[:n], data[chunkSize+5:chunkSize+105]) {
			t.Fatalf("ReadAt without part %d: %v", i, err)
		}
		f.Close()
	}
	for i := range dirs {
		filepath.Walk(dirs[i], func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				raw, _ := os.ReadFile(p)
				if bytes.HasPrefix(raw, []byte("age-encryption.org")) && strings.HasSuffix(p, ".c0") {
					t.Fatalf("C shard %s starts like an age file", p)
				}
			}
			return nil
		})
	}
}

func TestSplitPart(t *testing.T) {
	fs := vfs.NewLocalFS("Local", "/")
	for i := range 3 {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, recoveryFile), []byte(splitRecoveryText(i)), 0o644)
		if got, ok := SplitPart(fs, dir); !ok || got != i {
			t.Fatalf("part %d: SplitPart = %d, %v", i+1, got, ok)
		}
	}
	plain := t.TempDir()
	os.WriteFile(filepath.Join(plain, recoveryFile), []byte(recoveryText(config{})), 0o644)
	if _, ok := SplitPart(fs, plain); ok {
		t.Fatal("a vault in a folder taken for a split part")
	}
	if _, ok := SplitPart(fs, t.TempDir()); ok {
		t.Fatal("an empty folder taken for a split part")
	}
}
