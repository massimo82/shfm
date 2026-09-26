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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/rsyncclient"
	"github.com/gokrazy/rsync/rsyncd"

	"shfm/internal/applog"
)

// nonPOSIXFS lists the filesystem types that can't store Unix permissions
// or symlinks: on those, rsync runs without -l/-p, otherwise every run
// would fail (or warn) trying to chmod or create links.
var nonPOSIXFS = map[string]bool{"vfat": true, "exfat": true, "msdos": true, "ntfs": true, "ntfs3": true, "fuseblk": true}

// rsyncArgs returns the rsync options for a one-way mirror onto a
// filesystem of type destFSType.
func rsyncArgs(destFSType string) []string {
	if nonPOSIXFS[destFSType] {
		return []string{"-rt", "--delete"}
	}
	return []string{"-rlpt", "--delete"}
}

// RsyncStats reports the bytes moved by an rsync run.
type RsyncStats struct {
	Read    int64 // bytes read from the receiver
	Written int64 // bytes sent to the receiver (literal data + checksums)
	Size    int64 // total size of the source files
}

// runRsync mirrors the local path src onto the local path dst with the
// delta-transfer algorithm of github.com/gokrazy/rsync, entirely
// in-process: the library's client acts as sender and its server, running
// in a goroutine, as receiver, joined by two io.Pipes. No rsync binary is
// executed, and Landlock restrictions (which would apply to the whole
// shfm process) are disabled. For a directory src the contents of src
// become the contents of dst (rsync's "src/" convention); a single file
// is mirrored into dst's parent folder, so dst must have src's base name
// (the library always treats the destination path as a folder).
// Cancelling ctx aborts the transfer by closing the pipes.
func runRsync(ctx context.Context, src, dst string, srcIsDir bool, destFSType string) (RsyncStats, error) {
	logw := newLogWriter()
	defer logw.closeLog()

	client, err := rsyncclient.New(rsyncArgs(destFSType),
		rsyncclient.WithSender(), rsyncclient.WithStderr(logw), rsyncclient.DontRestrict())
	if err != nil {
		return RsyncStats{}, fmt.Errorf("rsync client: %w", err)
	}
	srv, err := rsyncd.NewServer(nil, rsyncd.WithStderr(logw), rsyncd.DontRestrict())
	if err != nil {
		return RsyncStats{}, fmt.Errorf("rsync server: %w", err)
	}

	if srcIsDir {
		src = strings.TrimSuffix(src, "/") + "/"
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return RsyncStats{}, err
		}
		// The receiver can't replace a non-empty folder with a file.
		if err := removeTypeChanges(src, dst); err != nil {
			return RsyncStats{}, err
		}
	} else {
		if filepath.Base(src) != filepath.Base(dst) {
			return RsyncStats{}, fmt.Errorf("rsync: single-file mirror needs the same name (%q vs %q)", filepath.Base(src), filepath.Base(dst))
		}
		if fi, err := os.Lstat(dst); err == nil && fi.IsDir() {
			if err := os.RemoveAll(dst); err != nil {
				return RsyncStats{}, err
			}
		}
		dst = filepath.Dir(dst)
	}

	// stdin/stdout from the point of view of the receiver.
	stdinrd, stdinwr := io.Pipe()
	stdoutrd, stdoutwr := io.Pipe()
	closeAll := func(err error) {
		stdinrd.CloseWithError(err)
		stdinwr.CloseWithError(err)
		stdoutrd.CloseWithError(err)
		stdoutwr.CloseWithError(err)
	}

	stop := context.AfterFunc(ctx, func() { closeAll(ctx.Err()) })
	defer stop()

	var wg sync.WaitGroup
	var srvErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn := rsyncd.NewConnection(stdinrd, stdoutwr, "<shfm-mirror>")
		srvErr = srv.HandleConnArgs(ctx, conn, nil, client.ServerCommandOptions(dst))
		// Unblock the client if the receiver bailed out early.
		stdoutwr.CloseWithError(srvErr)
	}()

	res, cliErr := client.Run(ctx, &rsync.BothCloser{ReadCloser: stdoutrd, WriteCloser: stdinwr}, []string{src})
	if cliErr != nil {
		closeAll(cliErr)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return RsyncStats{}, ctx.Err()
	}
	if err := errors.Join(cliErr, srvErr); err != nil {
		return RsyncStats{}, fmt.Errorf("rsync: %w", err)
	}
	var st RsyncStats
	if res != nil && res.Stats != nil {
		st = RsyncStats{Read: res.Stats.Read, Written: res.Stats.Written, Size: res.Stats.Size}
	}
	return st, nil
}

// removeTypeChanges deletes, under dst, every entry whose counterpart in
// src has a different kind (folder vs. anything else), so rsync can then
// recreate it with the right type.
func removeTypeChanges(src, dst string) error {
	return filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dst, p)
		if rel == "." {
			return nil
		}
		si, err := os.Lstat(filepath.Join(src, rel))
		if err != nil {
			// Missing in src: --delete takes care of it.
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if si.IsDir() != d.IsDir() {
			if err := os.RemoveAll(p); err != nil {
				return err
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
}

// logWriter forwards the library's diagnostic output, line by line, to
// shfm's own log instead of the terminal (where it would corrupt the TUI).
type logWriter struct {
	pw   *io.PipeWriter
	done chan struct{}
}

func newLogWriter() *logWriter {
	pr, pw := io.Pipe()
	w := &logWriter{pw: pw, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			applog.Debug("rsync", "msg", sc.Text())
		}
		io.Copy(io.Discard, pr)
	}()
	return w
}

func (w *logWriter) Write(p []byte) (int, error) { return w.pw.Write(p) }

// Close is a no-op for the library (which may "close" its stderr); the
// writer is really closed by closeLog once the transfer is over.
func (w *logWriter) Close() error { return nil }

func (w *logWriter) closeLog() {
	w.pw.Close()
	<-w.done
}
