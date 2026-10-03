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
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"

	"shfm/internal/vfs"
)

func TestMain(m *testing.M) {
	scryptWorkFactor = 10 // cheap: the tests unlock many vaults
	os.Exit(m.Run())
}

const pw = "correct horse battery staple"

// newVault creates a vault in a new folder over a local backend and
// returns the backend, the folder and the unlocked vault's FS.
func newVault(t *testing.T, opts Options) (vfs.FileSystem, string, *Vault, vfs.FileSystem) {
	t.Helper()
	be := vfs.NewLocalFS("test", "/")
	return newVaultOn(t, be, opts)
}

func newVaultOn(t *testing.T, be vfs.FileSystem, opts Options) (vfs.FileSystem, string, *Vault, vfs.FileSystem) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vault")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(be, dir, pw, opts); err != nil {
		t.Fatalf("Create: %v", err)
	}
	v, err := Unlock(be, dir, pw)
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	return be, dir, v, v.FS()
}

func writeFile(t *testing.T, fs vfs.FileSystem, p string, data []byte) {
	t.Helper()
	w, err := fs.Create(p)
	if err != nil {
		t.Fatalf("Create %s: %v", p, err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("Write %s: %v", p, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close %s: %v", p, err)
	}
}

func readFile(t *testing.T, fs vfs.FileSystem, p string) []byte {
	t.Helper()
	r, err := fs.Open(p)
	if err != nil {
		t.Fatalf("Open %s: %v", p, err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Read %s: %v", p, err)
	}
	return data
}

func names(t *testing.T, fs vfs.FileSystem, p string) []string {
	t.Helper()
	es, err := fs.List(p)
	if err != nil {
		t.Fatalf("List %s: %v", p, err)
	}
	var out []string
	for _, e := range es {
		n := e.Name
		if e.IsDir {
			n += "/"
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// backendFiles lists every file under dir on the local disk, relative.
func backendFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && p != dir {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func bothModes(t *testing.T, f func(t *testing.T, opts Options)) {
	for _, opts := range []Options{{Names: NamesEncrypted}, {Names: NamesPlain}} {
		t.Run(string(opts.Names), func(t *testing.T) { f(t, opts) })
	}
}

func TestCreateRefusesNonEmptyFolder(t *testing.T) {
	be := vfs.NewLocalFS("test", "/")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x"), nil, 0o644)
	if _, err := Create(be, dir, pw, Options{}); !errors.Is(err, ErrDirNotEmpty) {
		t.Fatalf("Create in a non-empty folder: %v, want ErrDirNotEmpty", err)
	}
	if _, err := Create(be, t.TempDir(), "", Options{}); err == nil {
		t.Fatal("Create with an empty password succeeded")
	}
}

func TestUnlock(t *testing.T) {
	be := vfs.NewLocalFS("test", "/")
	dir := t.TempDir()
	key, err := Create(be, dir, pw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !IsVault(be, dir) || IsVault(be, t.TempDir()) {
		t.Fatal("IsVault is wrong")
	}
	if !strings.HasPrefix(key, "AGE-SECRET-KEY-1") {
		t.Fatalf("recovery key %q", key)
	}
	if _, err := Unlock(be, dir, "wrong"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := Unlock(be, t.TempDir(), pw); !errors.Is(err, ErrNotVault) {
		t.Fatalf("not a vault: %v", err)
	}
	v, err := UnlockWithKey(be, dir, key)
	if err != nil {
		t.Fatalf("UnlockWithKey: %v", err)
	}
	if got, _ := v.RecoveryKey(); got != key {
		t.Fatal("RecoveryKey differs from the key Create returned")
	}
	other, _ := age.GenerateX25519Identity()
	if _, err := UnlockWithKey(be, dir, other.String()); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("someone else's key: %v", err)
	}
	if _, err := UnlockWithKey(be, dir, "garbage"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("garbage key: %v", err)
	}
}

func TestNewerFormatRefused(t *testing.T) {
	be := vfs.NewLocalFS("test", "/")
	dir := t.TempDir()
	Create(be, dir, pw, Options{})
	cfg, _ := os.ReadFile(filepath.Join(dir, configFile))
	os.WriteFile(filepath.Join(dir, configFile), bytes.Replace(cfg, []byte(`"version": 1`), []byte(`"version": 2`), 1), 0o644)
	if _, err := Unlock(be, dir, pw); !errors.Is(err, ErrNewerFormat) {
		t.Fatalf("Unlock of a newer vault: %v", err)
	}
}

func TestChangePassword(t *testing.T) {
	be, dir, v, fs := newVault(t, Options{})
	writeFile(t, fs, "/a.txt", []byte("hello"))
	if err := v.ChangePassword("new password"); err != nil {
		t.Fatal(err)
	}
	if _, err := Unlock(be, dir, pw); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("old password after the change: %v", err)
	}
	v2, err := Unlock(be, dir, "new password")
	if err != nil {
		t.Fatalf("new password: %v", err)
	}
	if got := readFile(t, v2.FS(), "/a.txt"); string(got) != "hello" {
		t.Fatalf("content after the password change: %q", got)
	}
	for _, f := range backendFiles(t, dir) {
		if strings.HasPrefix(f, identityFile+".") {
			t.Fatalf("left behind: %s (would keep the old password working)", f)
		}
	}
}

// An interrupted password change leaves identity.age.tmp only: it opens.
func TestIdentityTmpFallback(t *testing.T) {
	be, dir, v, _ := newVault(t, Options{})
	v.ChangePassword("new password")
	os.Rename(filepath.Join(dir, identityFile), filepath.Join(dir, identityFile+tmpSuffix))
	if _, err := Unlock(be, dir, "new password"); err != nil {
		t.Fatalf("Unlock from identity.age.tmp: %v", err)
	}
}

func TestReadWrite(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		_, _, _, fs := newVault(t, opts)
		for _, n := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 17} {
			data := randBytes(n)
			p := fmt.Sprintf("/f%d.bin", n)
			writeFile(t, fs, p, data)
			if got := readFile(t, fs, p); !bytes.Equal(got, data) {
				t.Fatalf("%d bytes: read back differs", n)
			}
			e, err := fs.Stat(p)
			if err != nil || e.Size != int64(n) || e.IsDir || e.SizeUnknown {
				t.Fatalf("Stat %s: %+v, %v", p, e, err)
			}
		}
	})
}

func TestSizeFormulas(t *testing.T) {
	_, _, v, _ := newVault(t, Options{})
	for _, n := range []int64{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 2 * chunkSize, 5*chunkSize + 3} {
		data, err := encryptBytes(randBytes(int(n)), v.st.rcpt)
		if err != nil {
			t.Fatal(err)
		}
		if c := cipherSize(v.st.overhead, n); c != int64(len(data)) {
			t.Fatalf("cipherSize(%d) = %d, real %d", n, c, len(data))
		}
		if p, ok := plainSize(v.st.overhead, int64(len(data))); !ok || p != n {
			t.Fatalf("plainSize(%d) = %d %v, want %d", len(data), p, ok, n)
		}
	}
	if _, ok := plainSize(v.st.overhead, v.st.overhead+5); ok {
		t.Fatal("plainSize accepted an impossible size")
	}
}

func TestOverwriteKeepsOldUntilClose(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		_, dir, _, fs := newVault(t, opts)
		writeFile(t, fs, "/a", []byte("old"))
		before := len(backendFiles(t, dir))
		w, _ := fs.Create("/a")
		w.Write([]byte("new content"))
		if got := readFile(t, fs, "/a"); string(got) != "old" {
			t.Fatalf("before Close: %q", got)
		}
		w.Close()
		if got := readFile(t, fs, "/a"); string(got) != "new content" {
			t.Fatalf("after Close: %q", got)
		}
		if after := len(backendFiles(t, dir)); after != before {
			t.Fatalf("backend files %d → %d: old content not removed", before, after)
		}
	})
}

func TestFoldersAndRenames(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		_, _, _, fs := newVault(t, opts)
		must := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		must(fs.Mkdir("/docs"))
		must(fs.Mkdir("/docs/sub"))
		if err := fs.Mkdir("/docs"); !errors.Is(err, os.ErrExist) {
			t.Fatalf("Mkdir twice: %v", err)
		}
		writeFile(t, fs, "/docs/sub/a.txt", []byte("A"))
		writeFile(t, fs, "/docs/b.txt", []byte("B"))
		must(fs.CreateEmptyFile("/empty"))
		if err := fs.CreateEmptyFile("/empty"); !errors.Is(err, os.ErrExist) {
			t.Fatalf("CreateEmptyFile twice: %v", err)
		}
		if got := names(t, fs, "/"); strings.Join(got, ",") != "docs/,empty" {
			t.Fatalf("root: %v", got)
		}
		if got := names(t, fs, "/docs"); strings.Join(got, ",") != "b.txt,sub/" {
			t.Fatalf("/docs: %v", got)
		}

		must(fs.Rename("/docs/b.txt", "/docs/c.txt"))     // same folder
		must(fs.Rename("/docs/c.txt", "/docs/sub/c.txt")) // into a subfolder
		must(fs.Rename("/docs/sub", "/moved"))            // a folder, to the root
		writeFile(t, fs, "/moved/d.txt", []byte("D"))     // its index still works
		must(fs.Rename("/moved/d.txt", "/moved/a.txt"))   // replacing a file
		if got := names(t, fs, "/moved"); strings.Join(got, ",") != "a.txt,c.txt" {
			t.Fatalf("/moved: %v", got)
		}
		if got := readFile(t, fs, "/moved/a.txt"); string(got) != "D" {
			t.Fatalf("replaced file: %q", got)
		}
		if err := fs.Rename("/docs", "/docs/inside"); err == nil {
			t.Fatal("moved a folder into itself")
		}
		if err := fs.Rename("/moved", "/empty"); !errors.Is(err, os.ErrExist) {
			t.Fatalf("folder over a file: %v", err)
		}
		if _, err := fs.Stat("/docs/sub"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old folder path: %v", err)
		}

		must(fs.Remove("/moved"))
		must(fs.Remove("/empty"))
		if got := names(t, fs, "/"); strings.Join(got, ",") != "docs/" {
			t.Fatalf("after Remove: %v", got)
		}
		if err := fs.Remove("/nope"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Remove of a missing file: %v", err)
		}
		if _, err := fs.Open("/docs"); err == nil {
			t.Fatal("opened a folder as a file")
		}
	})
}

// With encrypted names, nothing on the backend gives a name or content away.
func TestNothingInClear(t *testing.T) {
	_, dir, _, fs := newVault(t, Options{})
	fs.Mkdir("/SecretFolder")
	writeFile(t, fs, "/SecretFolder/SecretName.txt", []byte("SecretContent"))
	for _, f := range backendFiles(t, dir) {
		if strings.Contains(f, "Secret") {
			t.Fatalf("name in clear on the backend: %s", f)
		}
		if data, err := os.ReadFile(filepath.Join(dir, f)); err == nil && bytes.Contains(data, []byte("Secret")) {
			t.Fatalf("clear text inside %s", f)
		}
	}
}

func TestPlainNames(t *testing.T) {
	_, dir, _, fs := newVault(t, Options{Names: NamesPlain})
	fs.Mkdir("/photos")
	writeFile(t, fs, "/photos/cat.jpg", []byte("meow"))
	if _, err := os.Stat(filepath.Join(dir, "photos", "cat.jpg.age")); err != nil {
		t.Fatalf("plain layout: %v", err)
	}
	if got := names(t, fs, "/"); strings.Join(got, ",") != "photos/" {
		t.Fatalf("root shows the vault's own files: %v", got)
	}
	for _, bad := range []string{"/identity", "/RECOVERY.txt", "/.shfm-x"} {
		dir := bad == "/RECOVERY.txt"
		var err error
		if dir {
			err = fs.Mkdir(bad)
		} else {
			_, err = fs.Create(bad)
		}
		if !errors.Is(err, ErrReservedName) {
			t.Fatalf("%s: %v, want ErrReservedName", bad, err)
		}
	}
	if _, err := fs.Create("/" + strings.Repeat("x", 252)); !errors.Is(err, ErrNameTooLong) {
		t.Fatalf("long name: %v", err)
	}
	// A file not written by the vault is not vault content.
	os.WriteFile(filepath.Join(dir, "photos", "stray.txt"), nil, 0o644)
	if got := names(t, fs, "/photos"); strings.Join(got, ",") != "cat.jpg" {
		t.Fatalf("/photos: %v", got)
	}
	// A name can't be both a file and a folder.
	writeFile(t, fs, "/x", nil)
	if err := fs.Mkdir("/x"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("folder named like a file: %v", err)
	}
}

// Every file is a standard age file: the library alone decrypts it with the
// recovery key, and identity.age with the password.
func TestStandardAgeFiles(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		_, dir, v, fs := newVault(t, opts)
		writeFile(t, fs, "/doc.txt", []byte("standard"))
		key, _ := v.RecoveryKey()
		id, _ := age.ParseX25519Identity(key)

		sid, _ := age.NewScryptIdentity(pw)
		raw, _ := os.ReadFile(filepath.Join(dir, identityFile))
		text, err := decryptBytes(raw, sid)
		if err != nil || !strings.Contains(string(text), key) {
			t.Fatalf("identity.age with the password: %v", err)
		}

		var blob string
		if opts.Names == NamesPlain {
			blob = filepath.Join(dir, "doc.txt.age")
		} else {
			raw, _ := os.ReadFile(filepath.Join(dir, indexFile))
			idx, err := decryptBytes(raw, id)
			if err != nil || !bytes.Contains(idx, []byte(`"name":"doc.txt"`)) {
				t.Fatalf(".index.age: %s, %v", idx, err)
			}
			for _, f := range backendFiles(t, dir) {
				if strings.HasSuffix(f, ".age") && !strings.HasPrefix(f, ".") && f != identityFile {
					blob = filepath.Join(dir, f)
				}
			}
		}
		raw, _ = os.ReadFile(blob)
		got, err := decryptBytes(raw, id)
		if err != nil || string(got) != "standard" {
			t.Fatalf("file with the recovery key: %q, %v", got, err)
		}
	})
}

func TestPostQuantum(t *testing.T) {
	_, _, v, fs := newVault(t, Options{PostQuantum: true})
	if !v.Options().PostQuantum || !strings.HasPrefix(v.st.cfg.Recipient, "age1pq1") {
		t.Fatalf("not a post-quantum vault: %s", v.st.cfg.Recipient[:12])
	}
	key, _ := v.RecoveryKey()
	if !strings.HasPrefix(key, "AGE-SECRET-KEY-PQ-1") {
		t.Fatalf("recovery key %q", key[:20])
	}
	data := randBytes(2*chunkSize + 5)
	writeFile(t, fs, "/pq.bin", data)
	if got := readFile(t, fs, "/pq.bin"); !bytes.Equal(got, data) {
		t.Fatal("read back differs")
	}
}

func TestTamperingDetected(t *testing.T) {
	_, dir, _, fs := newVault(t, Options{Names: NamesPlain})
	writeFile(t, fs, "/a", randBytes(3*chunkSize))
	p := filepath.Join(dir, "a.age")
	raw, _ := os.ReadFile(p)
	raw[len(raw)/2] ^= 1
	os.WriteFile(p, raw, 0o644)
	r, err := fs.Open("/a")
	if err == nil {
		_, err = io.ReadAll(r)
		r.Close()
	}
	if !errors.Is(err, ErrDamaged) {
		t.Fatalf("flipped bit: %v, want ErrDamaged", err)
	}
	// Truncated: the last chunk is missing.
	os.WriteFile(p, raw[:len(raw)-chunkSize], 0o644)
	r, err = fs.Open("/a")
	if err == nil {
		_, err = io.ReadAll(r)
		r.Close()
	}
	if !errors.Is(err, ErrDamaged) {
		t.Fatalf("truncated: %v, want ErrDamaged", err)
	}
}

func TestIndexRecovery(t *testing.T) {
	be, dir, _, fs := newVault(t, Options{})
	writeFile(t, fs, "/one", []byte("1"))
	writeFile(t, fs, "/two", []byte("2"))
	idx := filepath.Join(dir, indexFile)

	// Interrupted between the two renames: only .tmp holds the newest.
	os.Rename(idx, idx+tmpSuffix)
	v, _ := Unlock(be, dir, pw)
	if got := names(t, v.FS(), "/"); strings.Join(got, ",") != "one,two" {
		t.Fatalf("from .tmp: %v", got)
	}
	os.Rename(idx+tmpSuffix, idx)

	// A damaged index: the previous version (.bak) is read.
	os.WriteFile(idx, []byte("garbage"), 0o644)
	v, _ = Unlock(be, dir, pw)
	if got := names(t, v.FS(), "/"); strings.Join(got, ",") != "one" {
		t.Fatalf("from .bak: %v", got)
	}

	// All gone: an error naming the damage, not an empty folder.
	os.WriteFile(idx+bakSuffix, []byte("garbage"), 0o644)
	v, _ = Unlock(be, dir, pw)
	if _, err := v.FS().List("/"); !errors.Is(err, ErrDamaged) {
		t.Fatalf("no readable index: %v, want ErrDamaged", err)
	}
}

func TestRandomAccess(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		_, _, _, fs := newVault(t, opts)
		data := randBytes(4*chunkSize + 100)
		writeFile(t, fs, "/video", data)
		ra := fs.(vfs.RandomAccessOpener)
		f, err := ra.OpenRandom("/video", os.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, off := range []int64{0, 10, chunkSize - 3, 2*chunkSize + 7, int64(len(data)) - 50} {
			buf := make([]byte, 100)
			n, err := f.ReadAt(buf, off)
			want := data[off:min(off+100, int64(len(data)))]
			if (err != nil && err != io.EOF) || !bytes.Equal(buf[:n], want) {
				t.Fatalf("ReadAt(%d): %d bytes, %v", off, n, err)
			}
		}
		if _, err := f.ReadAt(make([]byte, 1), int64(len(data))); err != io.EOF {
			t.Fatalf("ReadAt at the end: %v", err)
		}
		if _, err := f.WriteAt([]byte("x"), 0); !errors.Is(err, syscall.EROFS) {
			t.Fatalf("WriteAt: %v", err)
		}
	})
}

func TestTimes(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		_, _, _, fs := newVault(t, opts)
		writeFile(t, fs, "/a", []byte("x"))
		when := time.Date(2020, 5, 17, 10, 0, 0, 0, time.UTC)
		if err := fs.(vfs.TimesSetter).Chtimes("/a", time.Time{}, when); err != nil {
			t.Fatal(err)
		}
		e, _ := fs.Stat("/a")
		if !e.ModTime.Equal(when) {
			t.Fatalf("ModTime %v, want %v", e.ModTime, when)
		}
	})
}

func TestDirSize(t *testing.T) {
	_, _, _, fs := newVault(t, Options{})
	fs.Mkdir("/d")
	writeFile(t, fs, "/d/a", make([]byte, 100))
	writeFile(t, fs, "/b", make([]byte, 20))
	ds, ok := fs.(vfs.DirSizer) // over the local backend, a DirSizer
	if !ok {
		t.Fatal("no DirSizer over the local backend")
	}
	size, count, err := ds.DirSize("/")
	if err != nil || size != 120 || count != 3 {
		t.Fatalf("DirSize: %d bytes, %d items, %v", size, count, err)
	}
}

func TestLock(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		_, _, v, fs := newVault(t, opts)
		writeFile(t, fs, "/a", []byte("x"))
		v.Lock()
		if _, err := fs.List("/"); !errors.Is(err, ErrLocked) {
			t.Fatalf("List after Lock: %v", err)
		}
		if _, err := fs.Open("/a"); !errors.Is(err, ErrLocked) {
			t.Fatalf("Open after Lock: %v", err)
		}
		if _, err := fs.Create("/b"); !errors.Is(err, ErrLocked) {
			t.Fatalf("Create after Lock: %v", err)
		}
		if _, err := v.RecoveryKey(); !errors.Is(err, ErrLocked) {
			t.Fatalf("RecoveryKey after Lock: %v", err)
		}
	})
}

func TestConcurrentWrites(t *testing.T) {
	_, _, _, fs := newVault(t, Options{})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			writeFile(t, fs, fmt.Sprintf("/f%02d", i), []byte{byte(i)})
		}()
	}
	wg.Wait()
	if got := names(t, fs, "/"); len(got) != 20 {
		t.Fatalf("%d files listed, want 20: %v", len(got), got)
	}
}

// limitedFS is a backend like MTP: no Rename, a single session, and the
// size of a file declared before its data.
type limitedFS struct {
	vfs.FileSystem // only the interface's methods: no local extras
	mu             sync.Mutex
	declared       map[string]int64
}

func (l *limitedFS) Rename(string, string) error { return vfs.ErrNotSupported }
func (l *limitedFS) SingleSession()              {}
func (l *limitedFS) CreateSized(p string, size int64) (io.WriteCloser, error) {
	l.mu.Lock()
	l.declared[p] = size
	l.mu.Unlock()
	w, err := l.FileSystem.Create(p)
	return &sizeCheck{WriteCloser: w, want: size}, err
}

type sizeCheck struct {
	io.WriteCloser
	n, want int64
}

func (s *sizeCheck) Write(b []byte) (int, error) {
	s.n += int64(len(b))
	return s.WriteCloser.Write(b)
}

func (s *sizeCheck) Close() error {
	if err := s.WriteCloser.Close(); err != nil {
		return err
	}
	if s.n != s.want {
		return fmt.Errorf("declared %d bytes, wrote %d", s.want, s.n)
	}
	return nil
}

func TestLimitedBackend(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		be := &limitedFS{FileSystem: vfs.NewLocalFS("mtp", "/"), declared: map[string]int64{}}
		_, _, _, fs := newVaultOn(t, be, opts)
		if _, ok := fs.(vfs.SingleSession); !ok {
			t.Fatal("SingleSession not passed on")
		}
		if _, ok := fs.(vfs.Redialer); ok {
			t.Fatal("Redialer over a backend without it")
		}
		if _, ok := fs.(vfs.DirSizer); ok {
			t.Fatal("DirSizer over a backend without it")
		}
		data := randBytes(2*chunkSize + 9)
		w, err := fs.(vfs.SizedCreator).CreateSized("/a", int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
		if err := w.Close(); err != nil {
			t.Fatalf("sized write: %v", err)
		}
		writeFile(t, fs, "/a", []byte("rewritten")) // overwrite, no Rename
		writeFile(t, fs, "/b", []byte("second file"))
		if got := readFile(t, fs, "/a"); string(got) != "rewritten" {
			t.Fatalf("/a: %q", got)
		}
		if got := names(t, fs, "/"); strings.Join(got, ",") != "a,b" {
			t.Fatalf("root: %v", got)
		}
		fs.Mkdir("/d")
		if err := fs.Rename("/a", "/d/a"); !errors.Is(err, vfs.ErrNotSupported) {
			t.Fatalf("move without backend Rename: %v, want ErrNotSupported", err)
		}
		w, _ = fs.(vfs.SizedCreator).CreateSized("/short", 10)
		w.Write([]byte("abc"))
		if err := w.Close(); err == nil {
			t.Fatal("short sized write succeeded")
		}
		if _, err := fs.Stat("/short"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed write left a file: %v", err)
		}
	})
}

// redialFS is a network backend: it can open a new connection.
type redialFS struct {
	vfs.FileSystem
	dials *int
}

func (r redialFS) Redial() (vfs.FileSystem, error) {
	*r.dials++
	return redialFS{FileSystem: vfs.NewLocalFS("net", "/"), dials: r.dials}, nil
}

func TestRedial(t *testing.T) {
	dials := 0
	be := redialFS{FileSystem: vfs.NewLocalFS("net", "/"), dials: &dials}
	_, _, v, fs := newVaultOn(t, be, Options{})
	writeFile(t, fs, "/a", []byte("x"))
	rd, ok := fs.(vfs.Redialer)
	if !ok {
		t.Fatal("Redialer not passed on")
	}
	fs2, err := rd.Redial()
	if err != nil || dials != 1 {
		t.Fatalf("Redial: %v (%d dials)", err, dials)
	}
	if got := readFile(t, fs2, "/a"); string(got) != "x" {
		t.Fatalf("through the new connection: %q", got)
	}
	if _, ok := fs2.(vfs.Redialer); !ok {
		t.Fatal("the redialed FS lost Redialer")
	}
	v.Lock()
	if _, err := rd.Redial(); !errors.Is(err, ErrLocked) {
		t.Fatalf("Redial after Lock: %v", err)
	}
}

func TestKind(t *testing.T) {
	_, _, _, fs := newVault(t, Options{})
	if fs.Kind() != vfs.KindVault || fs.Root() != "/" || fs.SupportsTrash() {
		t.Fatal("wrong Kind/Root/SupportsTrash")
	}
	if _, ok := fs.(vfs.LocalPath); ok {
		t.Fatal("a vault must never expose local paths")
	}
}

// A vault's signature (vault.json and identity.age in one folder) can't be
// stored in a vault, however the files arrive: either one alone can.
func TestNoVaultInVault(t *testing.T) {
	bothModes(t, func(t *testing.T, opts Options) {
		_, _, _, fs := newVault(t, opts)
		writeFile(t, fs, "/vault.json", []byte("{}"))
		if _, err := fs.Create("/identity.age"); !errors.Is(err, ErrVaultInVault) {
			t.Fatalf("identity.age next to vault.json: %v", err)
		}
		fs.Mkdir("/d")
		writeFile(t, fs, "/d/identity.age", []byte("x"))
		if _, err := fs.Create("/d/vault.json"); !errors.Is(err, ErrVaultInVault) {
			t.Fatalf("vault.json next to identity.age: %v", err)
		}
		writeFile(t, fs, "/d/other", []byte("y"))
		if err := fs.Rename("/d/other", "/d/vault.json"); !errors.Is(err, ErrVaultInVault) {
			t.Fatalf("renamed to vault.json next to identity.age: %v", err)
		}
		if err := fs.Rename("/vault.json", "/d/vault.json"); !errors.Is(err, ErrVaultInVault) && !errors.Is(err, vfs.ErrNotSupported) {
			t.Fatalf("moved next to identity.age: %v", err)
		}
		// Renaming one into the other is no pair.
		if err := fs.Rename("/d/identity.age", "/d/renamed"); err != nil {
			t.Fatal(err)
		}
		writeFile(t, fs, "/d/vault.json", []byte("{}")) // alone now: fine
	})
}
