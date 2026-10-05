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

package fileops

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"shfm/internal/archive"
	"shfm/internal/vfs"
)

// byteLog records what Progress.OnBytes reports.
type byteLog struct {
	mu   sync.Mutex
	last Bytes
	n    int
}

func (l *byteLog) on(b Bytes) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last = b
	l.n++
}

func writeTestFile(t *testing.T, p string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCopyReportsBytes: a copy reports its bytes and files, out of the
// totals the scan finds, files of several chunks included.
func TestCopyReportsBytes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(src, "dir", "big"), 3*copyChunk+5)
	writeTestFile(t, filepath.Join(src, "dir", "sub", "small"), 10)
	writeTestFile(t, filepath.Join(src, "single"), 7)
	fs := vfs.NewLocalFS("Local", "/")
	var log byteLog
	res := Copy([]Item{{fs, filepath.Join(src, "dir")}, {fs, filepath.Join(src, "single")}}, fs, dst,
		&Progress{OnBytes: log.on})
	if len(res.Errors) > 0 || res.Done != 2 {
		t.Fatalf("Copy: %+v", res)
	}
	want := Bytes{Done: 3*copyChunk + 22, Total: 3*copyChunk + 22, Files: 3, FilesTotal: 3}
	if log.last != want {
		t.Errorf("last report %+v, want %+v", log.last, want)
	}
}

// TestCopyCancelledWithinFile: cancelling stops even halfway through a
// file, which is removed.
func TestCopyCancelledWithinFile(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(src, "big"), 4*copyChunk)
	fs := vfs.NewLocalFS("Local", "/")
	var polls atomic.Int32
	// Cancelled once the copy of the file has started: the first poll is
	// Copy's own, before the item.
	cancelled := func() bool { return polls.Add(1) > 2 }
	res := Copy([]Item{{fs, filepath.Join(src, "big")}}, fs, dst, &Progress{Cancelled: cancelled})
	if !res.Cancelled || len(res.Errors) > 0 {
		t.Fatalf("Copy: %+v, want cancelled without errors", res)
	}
	if _, err := os.Stat(filepath.Join(dst, "big")); !os.IsNotExist(err) {
		t.Errorf("the half-copied file is still there: %v", err)
	}
}

// TestMoveByRenameReportsNoBytes: a move within one backend renames, and
// has no bytes to measure.
func TestMoveByRenameReportsNoBytes(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	writeTestFile(t, filepath.Join(src, "f"), 100)
	fs := vfs.NewLocalFS("Local", "/")
	var log byteLog
	res := Move([]Item{{fs, filepath.Join(src, "f")}}, fs, dst, &Progress{OnBytes: log.on})
	if res.Done != 1 {
		t.Fatalf("Move: %+v", res)
	}
	if log.last != (Bytes{}) {
		t.Errorf("last report %+v, want nothing to copy", log.last)
	}
}

// sentWriter is a writer whose data reaches the destination later, as a
// cloud upload's.
type sentWriter struct {
	bytes.Buffer
	sent atomic.Int64
}

func (w *sentWriter) Sent() int64 { return w.sent.Load() }

// TestCopyDataCountsSent: a copy into a writer whose data arrives later
// counts what arrived, never more than was written.
func TestCopyDataCountsSent(t *testing.T) {
	var log byteLog
	m := &meter{prog: &Progress{OnBytes: log.on}, known: true, total: 1000, stop: make(chan struct{})}
	w := &sentWriter{}
	if _, err := copyData(w, bytes.NewReader(make([]byte, 1000)), m, nil); err != nil {
		t.Fatal(err)
	}
	m.report()
	if log.last.Done != 0 {
		t.Errorf("nothing sent yet: Done = %d", log.last.Done)
	}
	w.sent.Store(400)
	m.report()
	if log.last.Done != 400 {
		t.Errorf("400 bytes sent: Done = %d", log.last.Done)
	}
	w.sent.Store(1200) // a resent chunk counted twice
	m.report()
	if log.last.Done != 1000 {
		t.Errorf("more sent than written: Done = %d, want 1000", log.last.Done)
	}
	m.fileDone(1000, true)
	m.report()
	if want := (Bytes{Done: 1000, Total: 1000, Files: 1, FilesTotal: 1}); log.last != want {
		t.Errorf("after the file: %+v, want %+v", log.last, want)
	}
}

// TestCreateArchiveReportsBytes: an archive's creation reports the bytes
// and files it read into it.
func TestCreateArchiveReportsBytes(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "dir", "a"), 2*copyChunk)
	writeTestFile(t, filepath.Join(src, "b"), 5)
	fs := vfs.NewLocalFS("Local", "/")
	var log byteLog
	res := CreateArchive([]Item{{fs, filepath.Join(src, "dir")}, {fs, filepath.Join(src, "b")}}, fs,
		filepath.Join(t.TempDir(), "out.tar.gz"), archive.KindTarGz, &Progress{OnBytes: log.on})
	if len(res.Errors) > 0 {
		t.Fatal(res.Error())
	}
	want := Bytes{Done: 2*copyChunk + 5, Total: 2*copyChunk + 5, Files: 2, FilesTotal: 2}
	if log.last != want {
		t.Errorf("last report %+v, want %+v", log.last, want)
	}
}
