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
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// pseudoFSTypes lists virtual/kernel filesystem types whose reported file
// sizes are not real bytes-on-disk and must never be summed as if they
// were: most famously /proc/kcore, which represents the kernel's entire
// virtual address space and reports a fixed, fictitious size of exactly
// 128TiB (2^47 bytes) on x86_64 — the unmistakable signature of this exact
// class of bug when it shows up as an absurd folder size. Per-process
// files like /proc/[pid]/mem have similar issues. This mirrors the
// "pseudoFS" set internal/drives already excludes from the local-disk
// list, for the same underlying reason.
var pseudoFSTypes = map[string]bool{
	"proc": true, "sysfs": true, "devtmpfs": true, "devpts": true, "tmpfs": true,
	"cgroup": true, "cgroup2": true, "pstore": true, "bpf": true, "tracefs": true,
	"debugfs": true, "securityfs": true, "mqueue": true, "hugetlbfs": true,
	"autofs": true, "rpc_pipefs": true, "binfmt_misc": true, "configfs": true,
	"fusectl": true,
}

// isOnPseudoFS reports whether path resides on a virtual/kernel filesystem
// whose reported file sizes shouldn't be trusted for a "how much disk space
// does this folder use" computation.
func isOnPseudoFS(path string) bool {
	fstype, ok := mountFSType(path)
	return ok && pseudoFSTypes[fstype]
}

// mountFSType returns the filesystem type of the mount point path resides
// on, by finding the longest-matching mount point prefix in /proc/mounts
// (the same "longest prefix wins" rule the kernel itself uses to resolve
// which mount governs a given path).
func mountFSType(path string) (fstype string, ok bool) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", false
	}
	defer f.Close()

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	bestLen := -1
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		mp := unescapeMountField(fields[1])
		matches := absPath == mp || strings.HasPrefix(absPath, strings.TrimSuffix(mp, "/")+"/") || mp == "/"
		if !matches {
			continue
		}
		if len(mp) > bestLen {
			bestLen = len(mp)
			fstype = fields[2]
			ok = true
		}
	}
	return fstype, ok
}

// unescapeMountField decodes the octal escape sequences /proc/mounts uses
// for spaces and other special characters in paths (e.g. \040 for a space).
func unescapeMountField(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, err := strconv.ParseInt(s[i+1:i+4], 8, 32); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
