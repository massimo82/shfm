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
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Mount describes the mounted filesystem a local path lives on.
type Mount struct {
	MountPoint string
	Device     string
	FSType     string
	// UUID is the filesystem UUID (from /dev/disk/by-uuid), which stays
	// the same wherever and whenever the filesystem gets mounted: empty
	// when it has none (tmpfs, network mounts, ...).
	UUID string
}

// mountsFile and byUUIDDir are variables so tests can point them at
// synthetic data.
var (
	mountsFile = "/proc/mounts"
	byUUIDDir  = "/dev/disk/by-uuid"
)

func readMounts() []Mount {
	f, err := os.Open(mountsFile)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Mount
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		out = append(out, Mount{Device: fields[0], MountPoint: unescapeMount(fields[1]), FSType: fields[2]})
	}
	return out
}

// uuidsByDevice maps each block device (symlinks resolved, e.g.
// /dev/sdb1) to its filesystem UUID.
func uuidsByDevice() map[string]string {
	des, err := os.ReadDir(byUUIDDir)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(des))
	for _, de := range des {
		dev, err := filepath.EvalSymlinks(filepath.Join(byUUIDDir, de.Name()))
		if err == nil {
			out[dev] = de.Name()
		}
	}
	return out
}

func resolveDevice(dev string) string {
	if r, err := filepath.EvalSymlinks(dev); err == nil {
		return r
	}
	return dev
}

// MountOf returns the mount containing the absolute local path (the one
// with the longest matching mount point), with its UUID when it has one.
func MountOf(path string) (Mount, bool) {
	path = filepath.Clean(path)
	var best Mount
	found := false
	for _, mt := range readMounts() {
		if !isUnder(path, mt.MountPoint) {
			continue
		}
		// ">=": a later mount on the same point shadows an earlier one.
		if !found || len(mt.MountPoint) >= len(best.MountPoint) {
			best, found = mt, true
		}
	}
	if !found {
		return Mount{}, false
	}
	best.UUID = uuidsByDevice()[resolveDevice(best.Device)]
	return best, true
}

// MountByUUID returns where the filesystem with the given UUID is
// currently mounted, if it is.
func MountByUUID(uuid string) (Mount, bool) {
	uuids := uuidsByDevice()
	for _, mt := range readMounts() {
		if uuids[resolveDevice(mt.Device)] == uuid {
			mt.UUID = uuid
			return mt, true
		}
	}
	return Mount{}, false
}

// isUnder reports whether path is dir itself or inside it.
func isUnder(path, dir string) bool {
	if dir == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == dir || strings.HasPrefix(path, dir+"/")
}
