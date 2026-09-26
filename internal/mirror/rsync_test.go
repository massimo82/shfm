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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// writeTree creates files (path → content) under root; a path ending in
// "/" creates an empty directory.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, content := range files {
		full := filepath.Join(root, p)
		if strings.HasSuffix(p, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// readTree returns every entry under root as path → content ("/" suffix
// and empty content for directories).
func readTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		b, err := os.ReadFile(p)
		out[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertSameTree(t *testing.T, src, dst string) {
	t.Helper()
	a, b := readTree(t, src), readTree(t, dst)
	var diffs []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			diffs = append(diffs, "missing "+k)
		} else if v != w {
			diffs = append(diffs, "differs "+k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			diffs = append(diffs, "extra "+k)
		}
	}
	sort.Strings(diffs)
	if len(diffs) > 0 {
		t.Fatalf("trees differ: %v", diffs)
	}
}

func TestRsyncMirrorDirectory(t *testing.T) {
	tmp := t.TempDir()
	src, dst := filepath.Join(tmp, "src"), filepath.Join(tmp, "dst")
	writeTree(t, src, map[string]string{
		"a.txt":         "alpha",
		"sub/b.txt":     "beta",
		"sub/deep/c.md": "gamma",
		"empty/":        "",
		"kind":          "was a file",
	})
	writeTree(t, dst, map[string]string{
		"stale.txt":     "only in dest",
		"sub/old/x.bin": "stale subtree",
		"kind/inner":    "was a dir",
	})
	ctx := t.Context()
	if _, err := runRsync(ctx, src, dst, true, "ext4"); err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, src, dst)

	// Modify, delete and add, then mirror again.
	big := strings.Repeat("0123456789", 10000)
	writeTree(t, src, map[string]string{"a.txt": "alpha v2", "big.dat": big})
	os.Remove(filepath.Join(src, "sub/b.txt"))
	if _, err := runRsync(ctx, src, dst, true, "ext4"); err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, src, dst)

	// Small change inside a big file: the delta transfer should send far
	// less than the file size.
	time.Sleep(1100 * time.Millisecond) // rsync's quick check is per-second
	writeTree(t, src, map[string]string{"big.dat": big[:50000] + "CHANGED" + big[50007:]})
	st, err := runRsync(ctx, src, dst, true, "ext4")
	if err != nil {
		t.Fatal(err)
	}
	assertSameTree(t, src, dst)
	if st.Written >= int64(len(big))/2 {
		t.Errorf("delta transfer sent %d bytes for a %d-byte file", st.Written, len(big))
	}
	t.Logf("stats after small change: %+v", st)
}

func TestRsyncMirrorSingleFile(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "in", "note.txt")
	dst := filepath.Join(tmp, "out", "note.txt")
	writeTree(t, tmp, map[string]string{"in/note.txt": "hello", "out/sibling.txt": "keep me"})
	for _, want := range []string{"hello", "hello again"} {
		writeTree(t, tmp, map[string]string{"in/note.txt": want})
		if _, err := runRsync(t.Context(), src, dst, false, "ext4"); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(dst); err != nil || string(got) != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
		time.Sleep(1100 * time.Millisecond)
	}
	// --delete must not touch the rest of the destination folder.
	if got, err := os.ReadFile(filepath.Join(tmp, "out", "sibling.txt")); err != nil || string(got) != "keep me" {
		t.Fatalf("sibling: got %q, %v", got, err)
	}
	if _, err := runRsync(t.Context(), src, filepath.Join(tmp, "out", "other.txt"), false, "ext4"); err == nil {
		t.Fatal("expected an error for a renamed single-file destination")
	}
}
