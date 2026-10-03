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
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"shfm/internal/vfs"
)

// dirFS is a fake network backend serving a local folder, standing in for
// SMB/NFS/SFTP. Every Redial returns a new dirFS on the same folder
// (sharing the counters), and a dropped connection is simulated by
// setting dropped: the connection that sees it fails every operation from
// then on, as a dead network connection would.
type dirFS struct {
	root   string
	shared *dirShared
	gen    int64 // which connection this is
}

type dirShared struct {
	dials   atomic.Int64
	dropGen atomic.Int64 // connections with gen <= dropGen are dead
}

var errConnLost = errors.New("remote connection has closed")

func newDirFS(root string) *dirFS {
	return &dirFS{root: root, shared: &dirShared{}}
}

func (d *dirFS) alive() error {
	if d.gen <= d.shared.dropGen.Load() {
		return errConnLost
	}
	return nil
}

func (d *dirFS) real(p string) string { return filepath.Join(d.root, filepath.FromSlash(p)) }

func (d *dirFS) Kind() vfs.Kind { return vfs.KindSMB }
func (d *dirFS) Label() string  { return "smb://fake/share" }
func (d *dirFS) Root() string   { return "/" }

func (d *dirFS) List(p string) ([]vfs.Entry, error) {
	if err := d.alive(); err != nil {
		return nil, err
	}
	des, err := os.ReadDir(d.real(p))
	if err != nil {
		return nil, err
	}
	var out []vfs.Entry
	for _, de := range des {
		info, err := de.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, vfs.Entry{Name: de.Name(), IsDir: de.IsDir(), Size: info.Size(), ModTime: info.ModTime()})
	}
	return out, nil
}

func (d *dirFS) Stat(p string) (vfs.Entry, error) {
	if err := d.alive(); err != nil {
		return vfs.Entry{}, err
	}
	info, err := os.Stat(d.real(p))
	if err != nil {
		return vfs.Entry{}, err
	}
	return vfs.Entry{Name: info.Name(), IsDir: info.IsDir(), Size: info.Size(), ModTime: info.ModTime()}, nil
}

func (d *dirFS) Mkdir(p string) error {
	if err := d.alive(); err != nil {
		return err
	}
	return os.Mkdir(d.real(p), 0o755)
}

func (d *dirFS) CreateEmptyFile(p string) error { return os.WriteFile(d.real(p), nil, 0o644) }

func (d *dirFS) Remove(p string) error {
	if err := d.alive(); err != nil {
		return err
	}
	// Recursive, like the real backends: the mount must never rely on it
	// for unlink/rmdir.
	return os.RemoveAll(d.real(p))
}

func (d *dirFS) Rename(o, n string) error {
	if err := d.alive(); err != nil {
		return err
	}
	return os.Rename(d.real(o), d.real(n))
}

func (d *dirFS) Open(p string) (io.ReadCloser, error)    { return os.Open(d.real(p)) }
func (d *dirFS) Create(p string) (io.WriteCloser, error) { return os.Create(d.real(p)) }
func (d *dirFS) Join(elem ...string) string {
	return filepath.ToSlash(filepath.Join(append([]string{"/"}, elem...)...))
}
func (d *dirFS) Dir(p string) string  { return filepath.ToSlash(filepath.Dir(p)) }
func (d *dirFS) Base(p string) string { return filepath.Base(p) }
func (d *dirFS) SupportsTrash() bool  { return false }
func (d *dirFS) Close() error         { return nil }

func (d *dirFS) Redial() (vfs.FileSystem, error) {
	gen := d.shared.dials.Add(1)
	return &dirFS{root: d.root, shared: d.shared, gen: gen}, nil
}

type dirFile struct {
	f  *os.File
	fs *dirFS
}

func (f *dirFile) ReadAt(p []byte, off int64) (int, error) {
	if err := f.fs.alive(); err != nil {
		return 0, err
	}
	return f.f.ReadAt(p, off)
}

func (f *dirFile) WriteAt(p []byte, off int64) (int, error) {
	if err := f.fs.alive(); err != nil {
		return 0, err
	}
	return f.f.WriteAt(p, off)
}

func (f *dirFile) Truncate(size int64) error { return f.f.Truncate(size) }
func (f *dirFile) Close() error              { return f.f.Close() }

func (d *dirFS) OpenRandom(p string, flag int, perm os.FileMode) (vfs.RandomAccessFile, error) {
	if err := d.alive(); err != nil {
		return nil, err
	}
	if perm == 0 {
		perm = 0o644
	}
	f, err := os.OpenFile(d.real(p), flag, perm)
	if err != nil {
		return nil, err
	}
	return &dirFile{f: f, fs: d}, nil
}

// mountForTest mounts a dirFS serving a fresh folder, returning that
// folder, the fake source and the manager; skips the test where FUSE isn't
// available (no /dev/fuse or fusermount3, e.g. in some containers).
func mountForTest(t *testing.T) (backing string, src *dirFS, mg *Manager, mnt string) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	backing = t.TempDir()
	src = newDirFS(backing)
	mg = NewManager(t.TempDir())
	t.Cleanup(mg.UnmountAll)
	root, err := mg.LocalPath(src, "/")
	if err != nil {
		t.Skipf("FUSE mount not available here: %v", err)
	}
	return backing, src, mg, root
}

func TestReadListAndSeek(t *testing.T) {
	backing, _, _, mnt := mountForTest(t)

	data := make([]byte, 3<<20+17) // spans many FUSE reads
	for i := range data {
		data[i] = byte(i * 7)
	}
	if err := os.MkdirAll(filepath.Join(backing, "films"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backing, "films", "film.avi"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(mnt, "films"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "film.avi" {
		t.Fatalf("ReadDir = %v, %v", entries, err)
	}
	info, err := os.Stat(filepath.Join(mnt, "films", "film.avi"))
	if err != nil || info.Size() != int64(len(data)) || info.IsDir() {
		t.Fatalf("Stat = %v, %v", info, err)
	}
	got, err := os.ReadFile(filepath.Join(mnt, "films", "film.avi"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("ReadFile: err %v, equal %v", err, bytes.Equal(got, data))
	}

	// Seek like a video player: a range in the middle, then near the end.
	f, err := os.Open(filepath.Join(mnt, "films", "film.avi"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, off := range []int64{2 << 20, 1234, int64(len(data)) - 10} {
		buf := make([]byte, 100)
		n, err := f.ReadAt(buf, off)
		want := data[off:min(off+100, int64(len(data)))]
		if (err != nil && err != io.EOF) || !bytes.Equal(buf[:n], want) {
			t.Fatalf("ReadAt(%d) = %d, %v", off, n, err)
		}
	}

	if _, err := os.Stat(filepath.Join(mnt, "missing.srt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat of a missing file = %v, want ENOENT", err)
	}
}

func TestWriteTruncateAndSaveByRename(t *testing.T) {
	backing, _, _, mnt := mountForTest(t)

	// A new file, written through the mount.
	if err := os.WriteFile(filepath.Join(mnt, "doc.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(backing, "doc.txt")); string(got) != "hello world" {
		t.Fatalf("backing file = %q", got)
	}

	// Overwriting with shorter content must truncate (O_TRUNC).
	if err := os.WriteFile(filepath.Join(mnt, "doc.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(backing, "doc.txt")); string(got) != "hi" {
		t.Fatalf("after overwrite, backing file = %q", got)
	}

	// truncate(2) on a path, with no open file.
	if err := os.Truncate(filepath.Join(mnt, "doc.txt"), 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(backing, "doc.txt")); string(got) != "h" {
		t.Fatalf("after truncate, backing file = %q", got)
	}

	// In-place write at an offset.
	f, err := os.OpenFile(filepath.Join(mnt, "doc.txt"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("ey"), 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got, _ := os.ReadFile(filepath.Join(backing, "doc.txt")); string(got) != "hey" {
		t.Fatalf("after WriteAt, backing file = %q", got)
	}

	// An editor's save: write a temp file, rename it over the original.
	tmp := filepath.Join(mnt, ".doc.txt.swp")
	if err := os.WriteFile(tmp, []byte("saved"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(mnt, "doc.txt")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(backing, "doc.txt")); string(got) != "saved" {
		t.Fatalf("after save by rename, backing file = %q", got)
	}
	if _, err := os.Stat(filepath.Join(backing, ".doc.txt.swp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file still there: %v", err)
	}

	// O_EXCL on an existing file.
	if _, err := os.OpenFile(filepath.Join(mnt, "doc.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); !errors.Is(err, os.ErrExist) {
		t.Fatalf("O_EXCL on existing file = %v, want EEXIST", err)
	}
}

func TestRemoveNeverRecursive(t *testing.T) {
	backing, _, _, mnt := mountForTest(t)

	if err := os.Mkdir(filepath.Join(mnt, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mnt, "dir", "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := syscall.Rmdir(filepath.Join(mnt, "dir")); err != syscall.ENOTEMPTY {
		t.Fatalf("rmdir of a non-empty folder = %v, want ENOTEMPTY", err)
	}
	if err := syscall.Unlink(filepath.Join(mnt, "dir")); err != syscall.EISDIR {
		t.Fatalf("unlink of a folder = %v, want EISDIR", err)
	}
	if _, err := os.Stat(filepath.Join(backing, "dir", "keep.txt")); err != nil {
		t.Fatalf("folder content lost: %v", err)
	}

	if err := os.Remove(filepath.Join(mnt, "dir", "keep.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(mnt, "dir")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(backing, "dir")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("folder not removed: %v", err)
	}
}

func TestReconnectsAfterConnectionLoss(t *testing.T) {
	backing, src, _, mnt := mountForTest(t)

	if err := os.WriteFile(filepath.Join(backing, "a.bin"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(mnt, "a.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// The mount's connection dies; the next read (nothing is cached yet)
	// reconnects and reopens the file on the new connection.
	src.shared.dropGen.Store(src.shared.dials.Load())
	buf := make([]byte, 4)
	if n, err := f.ReadAt(buf, 3); err != nil || string(buf[:n]) != "3456" {
		t.Fatalf("ReadAt after connection loss = %q, %v", buf[:n], err)
	}
	if got := src.shared.dials.Load(); got != 2 {
		t.Fatalf("dials = %d, want 2 (mount + one reconnection)", got)
	}

	// New lookups work on the new connection too.
	if err := os.WriteFile(filepath.Join(backing, "b.bin"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(mnt, "b.bin")); err != nil || string(got) != "b" {
		t.Fatalf("ReadFile after reconnection = %q, %v", got, err)
	}
}

func TestUnmountAllRemovesMountPoints(t *testing.T) {
	_, src, mg, mnt := mountForTest(t)

	again, err := mg.LocalPath(src, "/x/y.avi")
	if err != nil || again != filepath.Join(mnt, "x", "y.avi") {
		t.Fatalf("second LocalPath = %q, %v: want the same mount", again, err)
	}
	if n := src.shared.dials.Load(); n != 1 {
		t.Fatalf("dials = %d, want 1 (mount reused)", n)
	}

	mg.UnmountAll()
	if _, err := os.Stat(mnt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mount point still there after UnmountAll: %v", err)
	}
	if _, err := os.Stat(mg.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("per-process folder still there after UnmountAll: %v", err)
	}
}

func TestCleanupStaleRemovesDeadProcessFolders(t *testing.T) {
	base := t.TempDir()

	// A process that has exited: its pid is free (or reused by a non-shfm
	// process, which counts the same).
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot run true:", err)
	}
	dead := filepath.Join(base, strconv.Itoa(cmd.Process.Pid), "smb-nas-video")
	if err := os.MkdirAll(dead, 0o700); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(base, strconv.Itoa(os.Getpid()), "smb-nas-music")
	if err := os.MkdirAll(mine, 0o700); err != nil {
		t.Fatal(err)
	}

	NewManager(base)
	if _, err := os.Stat(filepath.Dir(dead)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead process folder not cleaned up: %v", err)
	}
	if _, err := os.Stat(mine); err != nil {
		t.Fatalf("own folder removed: %v", err)
	}
}

func TestDirName(t *testing.T) {
	for label, want := range map[string]string{
		"smb://nas/video":           "video on nas",
		"sftp://max@host":           "max@host",
		"nfs://10.0.0.2/srv/nfs/":   "nfs on 10.0.0.2",
		"mtp://Pixel 7":             "Pixel 7",
		"smb://nas/.hidden":         "hidden on nas",
		"mtp://..":                  "source",
		"mtp://a\nb":                "a-b",
		"://":                       "source",
		"gdrive://me@example.com":   "Google Drive of me@example.com",
		"onedrive://me@outlook.com": "Microsoft OneDrive of me@outlook.com",
	} {
		if got := dirName(label); got != want {
			t.Errorf("dirName(%q) = %q, want %q", label, got, want)
		}
	}
}

func (d *dirFS) Chtimes(p string, atime, mtime time.Time) error {
	if err := d.alive(); err != nil {
		return err
	}
	if atime.IsZero() {
		atime = mtime
	}
	return os.Chtimes(d.real(p), atime, mtime)
}

// sessionFS is a single-session source (like an MTP device): it can't be
// redialed, so the mount shares it with the UI. The embedded interface
// hides dirFS's Redial.
type sessionFS struct {
	base
	closed    atomic.Bool
	dead      atomic.Bool // stops responding, as an unplugged device
	noRename  bool        // can't rename natively
	spaceUsed bool
}

type base interface {
	vfs.FileSystem
	vfs.RandomAccessOpener
	vfs.TimesSetter
}

func (s *sessionFS) SingleSession() {}
func (s *sessionFS) Close() error   { s.closed.Store(true); return nil }

func (s *sessionFS) Space(string) (uint64, uint64, error) {
	if s.dead.Load() {
		return 0, 0, errConnLost
	}
	s.spaceUsed = true
	return 64 << 30, 10 << 30, nil
}

func (s *sessionFS) Rename(o, n string) error {
	if s.noRename {
		return vfs.ErrNotSupported
	}
	return s.base.Rename(o, n)
}

func newSessionFS(t *testing.T) (*sessionFS, string) {
	t.Helper()
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	backing := t.TempDir()
	// A live connection: a dirFS's own gen 0 counts as dropped.
	conn, _ := newDirFS(backing).Redial()
	return &sessionFS{base: conn.(*dirFS)}, backing
}

func TestSharedSessionOwnership(t *testing.T) {
	src, backing := newSessionFS(t)
	if err := os.WriteFile(filepath.Join(backing, "song.mp3"), []byte("la la"), 0o644); err != nil {
		t.Fatal(err)
	}
	mg := NewManager(t.TempDir())
	defer mg.UnmountAll()

	local, err := mg.LocalPath(src, "/song.mp3")
	if err != nil {
		t.Skipf("FUSE mount not available here: %v", err)
	}
	if got, err := os.ReadFile(local); err != nil || string(got) != "la la" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	if n := src.base.(*dirFS).shared.dials.Load(); n != 1 { // newSessionFS's own
		t.Fatalf("a single-session source was redialed %d times", n)
	}

	// The UI still owns the session: unmounting leaves it open.
	mg.UnmountAll()
	if src.closed.Load() {
		t.Fatal("unmounting closed a session the UI still uses")
	}

	// The UI lets go: the mount takes over and closes it when unmounted.
	if _, err := mg.LocalPath(src, "/song.mp3"); err != nil {
		t.Fatal(err)
	}
	if !mg.Release(src) {
		t.Fatal("Release = false for a session backing a mount")
	}
	if src.closed.Load() {
		t.Fatal("Release closed the session")
	}
	if got, err := os.ReadFile(local); err != nil || string(got) != "la la" {
		t.Fatalf("after Release, ReadFile = %q, %v", got, err)
	}

	// Reopening the source takes the session back.
	if got := mg.Session(src.Kind(), src.Label()); got != vfs.FileSystem(src) {
		t.Fatalf("Session = %v, want the mount's session", got)
	}
	mg.UnmountAll()
	if src.closed.Load() {
		t.Fatal("unmounting closed a session the UI took back")
	}

	if _, err := mg.LocalPath(src, "/"); err != nil {
		t.Fatal(err)
	}
	mg.Release(src)
	mg.UnmountAll()
	if !src.closed.Load() {
		t.Fatal("a released session wasn't closed with its mount")
	}
	if mg.Release(src) {
		t.Fatal("Release = true with no mount left")
	}
}

func TestSessionDropsDeadDevice(t *testing.T) {
	src, _ := newSessionFS(t)
	mg := NewManager(t.TempDir())
	defer mg.UnmountAll()
	mnt, err := mg.LocalPath(src, "/")
	if err != nil {
		t.Skipf("FUSE mount not available here: %v", err)
	}
	mg.Release(src)
	src.dead.Store(true) // unplugged

	if got := mg.Session(src.Kind(), src.Label()); got != nil {
		t.Fatalf("Session = %v for a dead device, want nil", got)
	}
	if !src.closed.Load() {
		t.Fatal("the dead session wasn't closed")
	}
	if _, err := os.Stat(mnt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mount point of the dead session still there: %v", err)
	}
}

func TestStatfsTimesAndRenameFallback(t *testing.T) {
	src, backing := newSessionFS(t)
	src.noRename = true
	mg := NewManager(t.TempDir())
	defer mg.UnmountAll()
	mnt, err := mg.LocalPath(src, "/")
	if err != nil {
		t.Skipf("FUSE mount not available here: %v", err)
	}

	var st syscall.Statfs_t
	if err := syscall.Statfs(mnt, &st); err != nil {
		t.Fatal(err)
	}
	if total := st.Blocks * uint64(st.Bsize); total != 64<<30 {
		t.Fatalf("statfs total = %d, want %d", total, uint64(64<<30))
	}
	if free := st.Bavail * uint64(st.Bsize); free != 10<<30 {
		t.Fatalf("statfs free = %d, want %d", free, uint64(10<<30))
	}

	if err := os.WriteFile(filepath.Join(mnt, "a.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2020, 5, 17, 10, 30, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(mnt, "a.txt"), when, when); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(backing, "a.txt")); err != nil || !info.ModTime().Equal(when) {
		t.Fatalf("backing mtime = %v, %v; want %v", info.ModTime(), err, when)
	}

	// No native rename: moved by copy+delete, replacing the destination.
	if err := os.WriteFile(filepath.Join(backing, "b.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(mnt, "a.txt"), filepath.Join(mnt, "b.txt")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(backing, "b.txt")); string(got) != "content" {
		t.Fatalf("after rename fallback, b.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(backing, "a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a.txt still there after the move: %v", err)
	}
	// A folder can't be moved that way: mv(1) gets EXDEV and copies it.
	if err := os.Mkdir(filepath.Join(backing, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Rename(filepath.Join(mnt, "dir"), filepath.Join(mnt, "dir2")); err != syscall.EXDEV {
		t.Fatalf("renaming a folder without native rename = %v, want EXDEV", err)
	}
}

// mountFSType returns the filesystem type of the mount at dir, as listed in
// /proc/self/mountinfo.
func mountFSType(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Skipf("no mountinfo: %v", err)
	}
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || unescape.Replace(fields[4]) != dir {
			continue
		}
		for i, f := range fields {
			if f == "-" && i+1 < len(fields) {
				return fields[i+1]
			}
		}
	}
	t.Fatalf("%s is not mounted", dir)
	return ""
}

func TestExposeMountsNetworkSourceForOtherApps(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	src := newDirFS(t.TempDir())
	mg := NewManager(t.TempDir())
	defer mg.UnmountAll()

	if err := mg.Expose(src); err != nil {
		t.Skipf("FUSE mount not available here: %v", err)
	}
	root, err := mg.LocalPath(src, "/")
	if err != nil {
		t.Fatal(err)
	}
	if n := src.shared.dials.Load(); n != 1 {
		t.Fatalf("dials = %d, want 1 (LocalPath reuses the exposed mount)", n)
	}
	if got := filepath.Base(root); got != "share on fake" {
		t.Fatalf("mount point named %q, want %q", got, "share on fake")
	}
	if got := mountFSType(t, root); got != "fuse."+networkSubtype {
		t.Fatalf("filesystem type = %q, want fuse.%s", got, networkSubtype)
	}
}

func TestExposeLeavesSingleSessionSourcesAlone(t *testing.T) {
	src, _ := newSessionFS(t)
	mg := NewManager(t.TempDir())
	defer mg.UnmountAll()

	if err := mg.Expose(src); !errors.Is(err, vfs.ErrNotSupported) {
		t.Fatalf("Expose = %v, want ErrNotSupported", err)
	}
	if _, err := os.Stat(mg.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Expose mounted a single-session source: %v", err)
	}
	root, err := mg.LocalPath(src, "/")
	if err != nil {
		t.Skipf("FUSE mount not available here: %v", err)
	}
	if got := mountFSType(t, root); got != "fuse.shfm" {
		t.Fatalf("filesystem type = %q, want fuse.shfm", got)
	}
}

func TestNoMountAfterClose(t *testing.T) {
	src := newDirFS(t.TempDir())
	mg := NewManager(t.TempDir())
	mg.Close() // shfm quit while a source was still connecting

	if err := mg.Expose(src); err == nil {
		t.Fatal("Expose mounted a source after Close")
	}
	if _, err := os.Stat(mg.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mount folder created after Close: %v", err)
	}
}

func TestExternalUnmountFreesTheName(t *testing.T) {
	_, src, mg, mnt := mountForTest(t)

	// Ejected from another application's file dialog.
	if out, err := exec.Command("fusermount3", "-u", mnt).CombinedOutput(); err != nil {
		t.Skipf("fusermount3 -u: %v %s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(mnt); errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mount point left behind after an external unmount")
		}
		time.Sleep(20 * time.Millisecond)
	}

	again, err := mg.LocalPath(src, "/")
	if err != nil {
		t.Fatal(err)
	}
	if again != mnt {
		t.Fatalf("remounted on %q, want the same name %q", again, mnt)
	}
}
