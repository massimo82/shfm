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

package vfs

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// fakeBlock builds, under a fake sysfs root, the device directory for
// the block device dev at devPath (e.g. "pci0/usb1/1-1/host0/block/sdb/sdb1"),
// with the disk's removable flag, and links /sys/dev/block/maj:min to it.
func fakeBlock(t *testing.T, root, devPath, majMin, removable string, partition bool) {
	t.Helper()
	dir := filepath.Join(root, "devices", devPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	disk := dir
	if partition {
		if err := os.WriteFile(filepath.Join(dir, "partition"), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		disk = filepath.Dir(dir)
	}
	if err := os.WriteFile(filepath.Join(disk, "removable"), []byte(removable+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "dev", "block", majMin)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
}

func TestDevRemovable(t *testing.T) {
	root := t.TempDir()
	old := sysRoot
	sysRoot = root
	t.Cleanup(func() { sysRoot = old })

	fakeBlock(t, root, "pci0/usb1/1-1/host0/block/sdb/sdb1", "8:17", "0", true) // USB disk, flag off
	fakeBlock(t, root, "pci0/mmc0/block/mmcblk0/mmcblk0p1", "179:1", "1", true) // SD card
	fakeBlock(t, root, "pci0/ata1/host1/block/sda/sda2", "8:2", "0", true)      // internal SATA
	fakeBlock(t, root, "pci0/usb2/2-1/host2/block/sdc", "8:32", "1", false)     // stick, no partitions
	fakeBlock(t, root, "pci0/nvme/block/nvme0n1", "259:0", "0", false)          // internal NVMe

	for _, c := range []struct {
		maj, min uint32
		want     bool
	}{
		{8, 17, true}, {179, 1, true}, {8, 2, false}, {8, 32, true}, {259, 0, false},
		{8, 99, false}, // not in sysfs
	} {
		if got := devRemovable(unix.Mkdev(c.maj, c.min)); got != c.want {
			t.Errorf("%d:%d: removable %v, want %v", c.maj, c.min, got, c.want)
		}
	}
}

func TestMountSource(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mountinfo")
	info := "22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw\n" +
		"80 22 0:52 / /run/media/u/MY\\040STICK rw,nosuid shared:40 - fuseblk /dev/sd\\040b1 rw,user_id=0\n"
	if err := os.WriteFile(p, []byte(info), 0o644); err != nil {
		t.Fatal(err)
	}
	old := mountInfoPath
	mountInfoPath = p
	t.Cleanup(func() { mountInfoPath = old })

	if got := mountSource(unix.Mkdev(0, 52)); got != "/dev/sd b1" {
		t.Errorf("fuse source %q", got)
	}
	if got := mountSource(unix.Mkdev(0, 53)); got != "" {
		t.Errorf("unknown device: source %q", got)
	}
}

// forceRemovable makes every path count as on a removable drive.
func forceRemovable(t *testing.T) {
	old := onRemovable
	onRemovable = func(string) bool { return true }
	t.Cleanup(func() { onRemovable = old })
}

func TestCreateOnRemovableFlushes(t *testing.T) {
	forceRemovable(t)
	dir := t.TempDir()
	data := bytes.Repeat([]byte("0123456789abcdef"), (flushEvery+12345)/16)
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	l := NewLocalFS("Local", dir)

	// Through ReadFrom (io.Copy from a file), as fileops copies.
	w, err := l.Create(filepath.Join(dir, "a"))
	if err != nil {
		t.Fatal(err)
	}
	ff, ok := w.(*flushFile)
	if !ok {
		t.Fatalf("Create returned %T, want *flushFile", w)
	}
	if _, ok := w.(io.ReaderFrom); !ok {
		t.Fatal("flushFile lost ReadFrom")
	}
	r, _ := os.Open(src)
	defer r.Close()
	if _, err := io.Copy(w, r); err != nil {
		t.Fatal(err)
	}
	if ff.pending >= flushEvery {
		t.Errorf("%d bytes pending, more than flushEvery", ff.pending)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Through Write.
	w, err = l.Create(filepath.Join(dir, "b"))
	if err != nil {
		t.Fatal(err)
	}
	for b := data; len(b) > 0; {
		n := min(len(b), 1<<20)
		if _, err := w.Write(b[:n]); err != nil {
			t.Fatal(err)
		}
		b = b[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"a", "b"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("%s: %d bytes, err %v; want %d bytes", name, len(got), err, len(data))
		}
	}

	if err := FlushRemovable(filepath.Join(dir, "missing", "file")); err != nil {
		t.Errorf("FlushRemovable: %v", err)
	}
}

func TestCreateElsewhereIsPlainFile(t *testing.T) {
	old := onRemovable
	onRemovable = func(string) bool { return false }
	t.Cleanup(func() { onRemovable = old })
	dir := t.TempDir()
	w, err := NewLocalFS("Local", dir).Create(filepath.Join(dir, "a"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, ok := w.(*os.File); !ok {
		t.Errorf("Create returned %T, want *os.File", w)
	}
}
