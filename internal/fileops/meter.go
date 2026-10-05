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
	"io"
	"sync"
	"sync/atomic"
	"time"

	"shfm/internal/vfs"
)

// meterTick is how often a meter reports.
const meterTick = 250 * time.Millisecond

// copyChunk is how much copyData copies between two looks at the
// progress and at cancellation. Each chunk still goes through io.Copy,
// so a copy between local files keeps using copy_file_range(2).
const copyChunk = 1 << 20

// Bytes is the progress Progress.OnBytes reports: the bytes and files
// copied so far, out of totals that are -1 while the items are still
// being measured.
type Bytes struct {
	Done, Total       int64
	Files, FilesTotal int
}

// meter measures the bytes an operation copies, for Progress.OnBytes:
// a scan sums the size of the items alongside the copy, which starts
// at once, and a ticker reports a few times a second. A nil meter, for
// a Progress without OnBytes, does nothing.
type meter struct {
	prog *Progress

	mu         sync.Mutex
	done       int64        // bytes of the files finished
	cur        func() int64 // the file being copied's, nil between files
	total      int64        // the size of what the scan found so far
	files      int          // files finished
	filesTotal int          // files the scan found so far
	known      bool         // the scan has finished: the totals are final

	stop chan struct{}
	wg   sync.WaitGroup
}

// startMeter starts measuring an operation on scan's items (files and
// folders, recursively). Call finish once the operation is over.
func startMeter(prog *Progress, scan []Item) *meter {
	if prog == nil || prog.OnBytes == nil {
		return nil
	}
	m := &meter{prog: prog, stop: make(chan struct{})}
	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		for _, it := range scan {
			if m.stopped() {
				return
			}
			m.addTotal(m.size(it.FS, it.Path))
		}
		m.mu.Lock()
		m.known = true
		m.mu.Unlock()
	}()
	go func() {
		defer m.wg.Done()
		t := time.NewTicker(meterTick)
		defer t.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
				m.report()
			}
		}
	}()
	return m
}

// finish stops the meter, waiting for its scan (which must not touch the
// sources once the operation is over), and reports the last count. A
// scan cut short by the end of the operation still makes the totals
// final: report raises them to what was copied.
func (m *meter) finish() {
	if m == nil {
		return
	}
	close(m.stop)
	m.wg.Wait()
	m.mu.Lock()
	m.known = true
	m.mu.Unlock()
	m.report()
}

func (m *meter) stopped() bool {
	select {
	case <-m.stop:
		return true
	default:
		return false
	}
}

func (m *meter) report() {
	m.mu.Lock()
	b := Bytes{Done: m.done, Total: m.total, Files: m.files, FilesTotal: m.filesTotal}
	if m.cur != nil {
		b.Done += m.cur()
	}
	if !m.known {
		b.Total, b.FilesTotal = -1, -1
	} else {
		// A file grew, or the scan couldn't size it (or see it).
		b.Total = max(b.Total, b.Done)
		b.FilesTotal = max(b.FilesTotal, b.Files)
	}
	m.mu.Unlock()
	m.prog.OnBytes(b)
}

// size is the size of the files at p, recursively, and how many they
// are. Links to folders aren't followed, as the copy may not follow them
// either; files of unknown size count as empty.
func (m *meter) size(fs vfs.FileSystem, p string) (int64, int) {
	st, err := fs.Stat(p)
	if err != nil {
		return 0, 0
	}
	if !st.IsDir {
		return entrySize(st), 1
	}
	if st.IsSymlink {
		return 0, 0
	}
	return m.dirSize(fs, p)
}

func (m *meter) dirSize(fs vfs.FileSystem, dir string) (n int64, files int) {
	children, err := fs.List(dir)
	if err != nil {
		return 0, 0
	}
	for _, c := range children {
		if m.stopped() {
			return n, files
		}
		switch {
		case !c.IsDir:
			n += entrySize(c)
			files++
		case !c.IsSymlink:
			dn, df := m.dirSize(fs, fs.Join(dir, c.Name))
			n += dn
			files += df
		}
	}
	return n, files
}

func entrySize(e vfs.Entry) int64 {
	if e.SizeUnknown || e.Size < 0 {
		return 0
	}
	return e.Size
}

// addTotal adds n bytes in files to copy, found by the scan or added on
// the way (a move that turns out to need copying).
func (m *meter) addTotal(n int64, files int) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.total += n
	m.filesTotal += files
	m.mu.Unlock()
}

// addItem adds the size of an item the scan left out.
func (m *meter) addItem(it Item) {
	if m != nil {
		m.addTotal(m.size(it.FS, it.Path))
	}
}

func (m *meter) setCur(cur func() int64) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.cur = cur
	m.mu.Unlock()
}

// fileDone ends the file in progress, counting n bytes as copied, and
// the file itself unless it failed.
func (m *meter) fileDone(n int64, ok bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.done += n
	m.cur = nil
	if ok {
		m.files++
	}
	m.mu.Unlock()
}

// reader counts what's read from r as copied.
func (m *meter) reader(r io.Reader) io.Reader {
	if m == nil {
		return r
	}
	return &meterReader{r: r, m: m}
}

type meterReader struct {
	r io.Reader
	m *meter
}

func (r *meterReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.m.mu.Lock()
	r.m.done += int64(n)
	r.m.mu.Unlock()
	return n, err
}

// copyData copies r into w a chunk at a time, stopping with errCancelled
// once cancelled, and returns how much it wrote. Until fileDone, m
// counts the bytes written — or, for a writer whose data reaches the
// destination later (vfs.SentReporter), those it has received.
func copyData(w io.Writer, r io.Reader, m *meter, cancelled func() bool) (int64, error) {
	var written atomic.Int64
	cur := written.Load
	if s, ok := w.(vfs.SentReporter); ok {
		cur = func() int64 { return min(s.Sent(), written.Load()) }
	}
	m.setCur(cur)
	for {
		if cancelled != nil && cancelled() {
			return written.Load(), errCancelled
		}
		n, err := io.Copy(w, io.LimitReader(r, copyChunk))
		written.Add(n)
		if err != nil || n < copyChunk {
			return written.Load(), err
		}
	}
}
