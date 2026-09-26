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

package drives

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeMounts points MountOf/MountByUUID at a synthetic /proc/mounts and
// /dev/disk/by-uuid, where each "device" is a regular file in tmp.
func fakeMounts(t *testing.T, mounts string, uuids map[string]string) string {
	t.Helper()
	tmp := t.TempDir()
	mf := filepath.Join(tmp, "mounts")
	if err := os.WriteFile(mf, []byte(mounts), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "by-uuid")
	os.Mkdir(dir, 0o755)
	for uuid, dev := range uuids {
		os.WriteFile(filepath.Join(tmp, dev), nil, 0o644)
		if err := os.Symlink(filepath.Join(tmp, dev), filepath.Join(dir, uuid)); err != nil {
			t.Fatal(err)
		}
	}
	oldM, oldU := mountsFile, byUUIDDir
	mountsFile, byUUIDDir = mf, dir
	t.Cleanup(func() { mountsFile, byUUIDDir = oldM, oldU })
	return tmp
}

func TestMountOfAndByUUID(t *testing.T) {
	tmp := fakeMounts(t, "", map[string]string{"AAAA-1111": "sda1", "BBBB-2222": "sdb1"})
	mounts := "" +
		tmp + "/sda1 / ext4 rw 0 0\n" +
		"tmpfs /tmp tmpfs rw 0 0\n" +
		tmp + "/sdb1 /run/media/u/My\\040Stick vfat rw 0 0\n"
	os.WriteFile(mountsFile, []byte(mounts), 0o644)

	m, ok := MountOf("/run/media/u/My Stick/docs/a.txt")
	if !ok || m.MountPoint != "/run/media/u/My Stick" || m.UUID != "BBBB-2222" || m.FSType != "vfat" {
		t.Fatalf("MountOf(stick) = %+v, %v", m, ok)
	}
	m, ok = MountOf("/home/u/x")
	if !ok || m.MountPoint != "/" || m.UUID != "AAAA-1111" {
		t.Fatalf("MountOf(home) = %+v, %v", m, ok)
	}
	if m, ok = MountOf("/tmp/x"); !ok || m.MountPoint != "/tmp" || m.UUID != "" {
		t.Fatalf("MountOf(tmp) = %+v, %v", m, ok)
	}
	if m, ok = MountByUUID("BBBB-2222"); !ok || m.MountPoint != "/run/media/u/My Stick" {
		t.Fatalf("MountByUUID = %+v, %v", m, ok)
	}
	if _, ok = MountByUUID("CCCC-3333"); ok {
		t.Fatal("MountByUUID found an unmounted filesystem")
	}
	if isUnder("/run/media/u/My Sticker", "/run/media/u/My Stick") {
		t.Fatal("isUnder matched a sibling with a common prefix")
	}
}
