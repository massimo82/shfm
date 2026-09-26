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

// Package mirror keeps a destination identical to a source (one-way
// mirror): new and changed files are copied, and everything in the
// destination that isn't in the source is deleted. Between two local
// sources (internal disks, removable drives) it can use the rsync
// delta-transfer algorithm (see rsync.go, in-process via
// github.com/gokrazy/rsync); everywhere else, or when asked to, it uses a
// simple generic engine over vfs.FileSystem (see generic.go).
package mirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"shfm/internal/applog"
	"shfm/internal/fileops"
	"shfm/internal/vfs"
)

// Side is one end of a mirror, resolved to an open source.
type Side struct {
	FS   vfs.FileSystem
	Path string // absolute path within FS
	// FSType is the filesystem type of a local side (e.g. "ext4",
	// "vfat"): the rsync options depend on the destination's.
	FSType string
}

// Options tunes a Run.
type Options struct {
	// UseRsync selects the rsync backend; honored only when CanUseRsync.
	UseRsync bool
	// StateFile is where the generic engine keeps what it copied last time
	// (see manifest.go); unused by rsync.
	StateFile string
}

// CanUseRsync reports whether the rsync backend can mirror between the
// two sources: both must be local (internal disks or removable drives).
func CanUseRsync(src, dst vfs.FileSystem) bool {
	return src.Kind() == vfs.KindLocal && dst.Kind() == vfs.KindLocal
}

// Run mirrors src onto dst. It never deletes anything when the source
// can't be read: a missing or unreadable source root fails the run
// untouched, and entries under a source folder that couldn't be listed
// are left alone. prog reports progress and is polled for cancellation.
func Run(src, dst Side, opts Options, prog *fileops.Progress) *fileops.Result {
	var res *fileops.Result
	if opts.UseRsync && CanUseRsync(src.FS, dst.FS) {
		res = runRsyncBackend(src, dst, prog)
	} else {
		res = runGeneric(src, dst, opts.StateFile, prog)
	}
	for _, err := range res.Errors {
		applog.Warn("mirror", "src", src.Path, "dst", dst.Path, "error", err)
	}
	return res
}

func runRsyncBackend(src, dst Side, prog *fileops.Progress) *fileops.Result {
	res := &fileops.Result{}
	fi, err := os.Stat(src.Path)
	if err != nil {
		return fail(res, prog, fmt.Errorf("source: %w", err))
	}
	const label = "rsync (delta transfer)"
	report(prog, 0, 1, label, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if prog != nil && prog.Cancelled != nil {
		go func() {
			tick := time.NewTicker(250 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if prog.Cancelled() {
						cancel()
						return
					}
				}
			}
		}()
	}

	st, err := runRsync(ctx, src.Path, dst.Path, fi.IsDir(), dst.FSType)
	switch {
	case errors.Is(err, context.Canceled):
		res.Cancelled = true
	case err != nil:
		res.Errors = append(res.Errors, err)
	default:
		res.Done = 1
	}
	name := label
	if err == nil {
		name = fmt.Sprintf("rsync: %s sent of %s", humanBytes(st.Written), humanBytes(st.Size))
	}
	report(prog, 1, 1, name, err)
	return res
}

// fail records an error that stops a run before any change is made,
// reporting it too so the UI can show why.
func fail(res *fileops.Result, prog *fileops.Progress, err error) *fileops.Result {
	res.Errors = append(res.Errors, err)
	report(prog, 0, 0, "", err)
	return res
}

func report(prog *fileops.Progress, done, total int, name string, err error) {
	if prog != nil && prog.OnItem != nil {
		prog.OnItem(done, total, name, err)
	}
}

func cancelled(prog *fileops.Progress) bool {
	return prog != nil && prog.Cancelled != nil && prog.Cancelled()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
