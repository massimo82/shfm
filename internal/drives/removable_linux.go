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

package drives

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// RemovableDevice represents a removable mass-storage device (or one of its
// partitions) discovered via sysfs (/sys/block), such as USB flash drives,
// SD card readers, external USB disks.
type RemovableDevice struct {
	Name       string // e.g. "sdb1"
	Path       string // e.g. "/dev/sdb1"
	SizeBytes  uint64
	Mounted    bool
	MountPoint string
	FSType     string // known only when already mounted
	// Vendor/Model identify the underlying disk hardware (see diskInfo),
	// for a human-recognizable label in the source picker.
	Vendor string
	Model  string
}

// ListRemovable lists removable devices found via the "removable" flag
// sysfs exposes for each block disk, including any partitions or, if the
// disk has none, the disk itself. Requires no external command (no
// udisksctl, no lsblk): reads only /sys/block and /proc/mounts.
func ListRemovable() ([]RemovableDevice, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	mounts := mountsByDevice()

	var out []RemovableDevice
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") ||
			strings.HasPrefix(name, "dm-") || strings.HasPrefix(name, "zram") {
			continue
		}
		flag, err := os.ReadFile(filepath.Join("/sys/block", name, "removable"))
		isRemovableFlag := err == nil && strings.TrimSpace(string(flag)) == "1"
		if !isRemovableFlag && !isUSBAttached(name) {
			// Skip it: not flagged removable by the kernel (many external
			// USB hard disks/SSDs — as opposed to flash "thumb" drives —
			// report removable=0, since that flag really means "the media
			// itself is removable", like a floppy or SD card, not "the
			// enclosure is hot-pluggable") AND not attached via the USB
			// bus either, so it doesn't belong in the removable-media
			// picker at all (e.g. an internal SATA/NVMe disk).
			continue
		}

		sub, _ := os.ReadDir(filepath.Join("/sys/block", name))
		var partitions []string
		for _, se := range sub {
			if !se.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join("/sys/block", name, se.Name(), "partition")); err == nil {
				partitions = append(partitions, se.Name())
			}
		}
		vendor, model := diskInfo(name)
		if len(partitions) == 0 {
			if dev := buildRemovableEntry(name, vendor, model, mounts); dev != nil {
				out = append(out, *dev)
			}
			continue
		}
		for _, p := range partitions {
			if dev := buildRemovableEntry(p, vendor, model, mounts); dev != nil {
				out = append(out, *dev)
			}
		}
	}
	return out, nil
}

// isUSBAttached reports whether the block device name (e.g. "sdb") is
// connected via the USB bus, by resolving its "device" symlink in sysfs
// and checking whether the resulting path passes through a USB bus
// directory. This catches external hard disks/SSDs in USB enclosures,
// which the kernel's own "removable" sysfs attribute frequently does NOT
// flag as removable (that attribute reflects whether the storage *medium*
// itself is removable, like a floppy disk or SD card — not whether the
// *enclosure*/connection is hot-pluggable) — exactly the same distinction
// desktop tools like GNOME Disks/udisks2 account for by also looking at
// the device's USB attachment (there, via the udev "ID_BUS" property).
func isUSBAttached(name string) bool {
	linkPath := filepath.Join("/sys/block", name, "device")
	target, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		return false
	}
	return strings.Contains(target, "/usb")
}

func buildRemovableEntry(name, vendor, model string, mounts map[string]mountInfo) *RemovableDevice {
	sizeData, err := os.ReadFile(filepath.Join("/sys/class/block", name, "size"))
	if err != nil {
		return nil
	}
	sectors, _ := strconv.ParseUint(strings.TrimSpace(string(sizeData)), 10, 64)
	if sectors == 0 {
		// No media inserted (e.g. an empty SD card reader).
		return nil
	}
	dev := &RemovableDevice{Name: name, Path: "/dev/" + name, SizeBytes: sectors * 512, Vendor: vendor, Model: model}
	if mi, ok := mounts["/dev/"+name]; ok {
		dev.Mounted = true
		dev.MountPoint = mi.mountPoint
		dev.FSType = mi.fsType
	}
	return dev
}

type mountInfo struct{ mountPoint, fsType string }

func mountsByDevice() map[string]mountInfo {
	res := map[string]mountInfo{}
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return res
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		if strings.HasPrefix(fields[0], "/dev/") {
			res[fields[0]] = mountInfo{mountPoint: unescapeMount(fields[1]), fsType: fields[2]}
		}
	}
	return res
}

// fsTypeCandidates returns the filesystems the running kernel knows how to
// handle for block devices (excluding "nodev" ones like proc/sysfs), with
// the most common types for removable devices listed first. This is the
// pure-Go equivalent of "mount -t auto"'s auto-detection: try known
// filesystems until the kernel accepts one for that device.
func fsTypeCandidates() []string {
	preferred := []string{"vfat", "exfat", "ntfs3", "ext4", "ext3", "ext2", "xfs", "btrfs", "iso9660", "udf", "hfsplus"}
	seen := make(map[string]bool, len(preferred))
	out := make([]string, 0, len(preferred)+8)
	for _, p := range preferred {
		out = append(out, p)
		seen[p] = true
	}
	f, err := os.Open("/proc/filesystems")
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			// Two-field lines start with "nodev" and denote "virtual"
			// filesystems (tmpfs, proc, sysfs, cgroup...) that do NOT
			// require a block device: the kernel accepts them even with a
			// nonexistent or irrelevant source, so they must be excluded
			// here — otherwise a "successful mount" could silently ignore
			// the requested removable device entirely.
			if len(fields) != 1 {
				continue
			}
			name := fields[0]
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// mountPointFor chooses (and creates) a folder to use as the mount point for
// a removable device, preferring /media (classic desktop behavior, requires
// root), then $XDG_RUNTIME_DIR, then the system temp folder.
func mountPointFor(name string) (string, error) {
	var candidates []string
	if os.Getuid() == 0 {
		candidates = append(candidates, filepath.Join("/media", name))
	}
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		candidates = append(candidates, filepath.Join(rt, "shfm", name))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "shfm-mounts", name))

	var lastErr error
	for _, c := range candidates {
		if err := os.MkdirAll(c, 0o755); err == nil {
			return c, nil
		} else {
			lastErr = err
		}
	}
	return "", lastErr
}

// TryAutoMount mounts devicePath, preferring the system's udisks2 daemon via
// D-Bus (see udisks_linux.go) — the same mechanism Nautilus/Thunar/GVfs use,
// which normally succeeds for removable media without root privileges
// thanks to udisks2's default polkit rules for the active local session.
// Falls back to a direct mount(2) syscall (which typically *does* require
// root) only if udisks2 is unavailable or refuses.
func TryAutoMount(devicePath, name string) (string, error) {
	mp, udisksErr := MountViaUDisks2(devicePath)
	if udisksErr == nil {
		return mp, nil
	}
	mp, directErr := tryDirectMount(devicePath, name)
	if directErr == nil {
		return mp, nil
	}
	return "", fmt.Errorf(
		"udisks2 mount failed (%v); direct mount(2) also failed (%v) — "+
			"you may need administrator privileges, or udisks2 may not be running",
		udisksErr, directErr)
}

// tryDirectMount mounts devicePath by calling the mount(2) syscall directly
// (package "syscall" of the Go standard library), trying in sequence the
// filesystems supported by the running kernel: no dependency on external
// commands such as mount(8), udisksctl, or libraries such as libblkid.
// Requires the privileges mount(2) needs (usually root, unless the system
// has particular configurations). Used as a fallback when udisks2 isn't
// available.
func tryDirectMount(devicePath, name string) (string, error) {
	mp, err := mountPointFor(name)
	if err != nil {
		return "", fmt.Errorf("could not create a mount point: %w", err)
	}
	var lastErr error
	for _, fstype := range fsTypeCandidates() {
		if err := syscall.Mount(devicePath, mp, fstype, 0, ""); err == nil {
			return mp, nil
		} else {
			lastErr = err
		}
	}
	os.Remove(mp)
	return "", fmt.Errorf("no recognized filesystem for %s (last error: %v)", devicePath, lastErr)
}

// Unmount unmounts mountPoint, preferring udisks2 via D-Bus (matching
// TryAutoMount's preference), falling back to a direct umount(2) syscall.
func Unmount(devicePath, mountPoint string) error {
	if err := UnmountViaUDisks2(devicePath); err == nil {
		return nil
	}
	return syscall.Unmount(mountPoint, 0)
}
