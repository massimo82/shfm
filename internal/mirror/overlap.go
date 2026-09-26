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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	"shfm/internal/vfs"
)

// ErrOverlap means a pair's destination is its source, or one is inside
// the other, as stored data: mirroring (or deleting the copy) would
// destroy the source.
var ErrOverlap = errors.New("the destination is the source itself, or one is inside the other")

// ErrOverlapUnknown means the check couldn't finish (too big a source to
// search, or a destination folder that can't be written to).
var ErrOverlapUnknown = errors.New("could not verify that the destination doesn't hold the source's data")

// Bounds on searching the source for the probe (see Overlap).
var (
	probeMaxDirs = 20000
	probeTimeout = 10 * time.Second
)

// Overlap checks whether dst is src, or one is inside the other, as the
// actual files rather than as paths: two local paths are compared by
// device and inode along their ancestors (symlinks and bind mounts
// included); otherwise, since two backends can reach the same files (a
// local folder shared over SMB, two connections to one server), a hidden
// empty probe file is created next to dst and looked for from the source
// side, then removed. It returns nil, ErrOverlap or ErrOverlapUnknown
// (also when cancelled, if non-nil, reports true while searching).
func Overlap(src, dst Side, cancelled func() bool) error {
	if src.FS.Kind() == vfs.KindLocal && dst.FS.Kind() == vfs.KindLocal {
		return localOverlap(src.Path, dst.Path)
	}
	if cancelled == nil {
		cancelled = func() bool { return false }
	}
	return probeOverlap(src, dst, cancelled)
}

func localOverlap(src, dst string) error {
	within := func(path, dir string) bool {
		di, err := os.Stat(dir)
		if err != nil {
			return false
		}
		for {
			if fi, err := os.Stat(path); err == nil && os.SameFile(fi, di) {
				return true
			}
			parent := filepath.Dir(path)
			if parent == path {
				return false
			}
			path = parent
		}
	}
	if within(dst, src) || within(src, dst) {
		return ErrOverlap
	}
	return nil
}

func probeOverlap(src, dst Side, cancelled func() bool) error {
	dfs, sfs := dst.FS, src.FS
	dstParent, dstName := dfs.Dir(dst.Path), dfs.Base(dst.Path)
	if dstParent == dst.Path {
		// dst is its backend's root: the probe goes in dst itself, and
		// finding it anywhere up from src means dst contains src.
		dstName = ""
	}
	b := make([]byte, 8)
	rand.Read(b)
	probe := ".shfm-mirror-probe-" + hex.EncodeToString(b)
	probePath := dfs.Join(dstParent, probe)
	if err := dfs.CreateEmptyFile(probePath); err != nil {
		return ErrOverlapUnknown
	}
	defer dfs.Remove(probePath)

	// dst's folder is src or one of its ancestors: dst overlaps src when
	// that folder is src itself (dst inside src) or dst is the ancestor's
	// child leading to src (dst is src or contains it).
	child := ""
	for dir, i := src.Path, 0; i < 256; i, dir = i+1, sfs.Dir(dir) {
		if _, err := sfs.Stat(sfs.Join(dir, probe)); err == nil {
			if child == "" || dstName == "" || child == dstName {
				return ErrOverlap
			}
			return nil
		}
		if sfs.Dir(dir) == dir {
			break
		}
		child = sfs.Base(dir)
	}

	// dst's folder deeper inside src: search src's folders for the probe.
	if e, err := sfs.Stat(src.Path); err != nil || !e.IsDir {
		return nil
	}
	deadline := time.Now().Add(probeTimeout)
	queue := []string{src.Path}
	for n := 0; len(queue) > 0; n++ {
		if n >= probeMaxDirs || time.Now().After(deadline) || cancelled() {
			return ErrOverlapUnknown
		}
		dir := queue[0]
		queue = queue[1:]
		entries, err := sfs.List(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			switch {
			case e.Name == probe:
				return ErrOverlap
			case e.IsDir && !e.IsSymlink:
				queue = append(queue, sfs.Join(dir, e.Name))
			}
		}
	}
	return nil
}
