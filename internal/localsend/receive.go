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

//go:build localsend

package localsend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"shfm/internal/fileops"
	"shfm/internal/localsend/protocol"
	"shfm/internal/vfs"
)

// idleTimeout ends a transfer whose sender sent nothing for that long:
// gone without cancelling (out of the network, closed).
const idleTimeout = 2 * time.Minute

// progressTick is how often a transfer reports its bytes.
const progressTick = 250 * time.Millisecond

var (
	errSenderGone      = errors.New("the sender gave up")
	errSenderCancelled = errors.New("cancelled by the sender")
	errSenderIdle      = errors.New("the sender stopped sending")
	errNoSession       = errors.New("no such transfer")
)

// request is a Request waiting for the user.
type request struct {
	s      *Service
	offer  protocol.Offer
	answer chan *receiveSession // nil: declined
	gone   chan struct{}        // closed once the request can't be answered
}

// prepare asks the user about an offer (see protocol.Handler).
func (s *Service) prepare(ctx context.Context, o protocol.Offer) ([]string, error) {
	s.mu.Lock()
	receive, busy := s.settings.Receive, s.recv != nil
	s.mu.Unlock()
	if !receive {
		return nil, protocol.ErrDeclined
	}
	if busy {
		return nil, protocol.ErrBusy
	}
	from := deviceOf(o.From)
	if o.CertFingerprint != "" {
		from.Fingerprint = o.CertFingerprint
	}
	if text, ok := protocol.Message(o.Files); ok {
		// The whole message is here: nothing to download, nothing to
		// answer but "received".
		if s.ev.Incoming != nil {
			s.ev.Incoming(&Request{From: from, IsMessage: true, Message: text})
		}
		return nil, nil
	}

	r := &request{s: s, offer: o, answer: make(chan *receiveSession), gone: make(chan struct{})}
	req := &Request{From: from, r: r}
	files := append([]protocol.File(nil), o.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	for _, f := range files {
		req.Files = append(req.Files, IncomingFile{Name: f.Name, Size: f.Size})
		req.Size += f.Size
	}
	s.mu.Lock()
	s.pending[o.SessionID] = r
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, o.SessionID)
		s.mu.Unlock()
	}()
	if s.ev.Incoming != nil {
		s.ev.Incoming(req)
	}

	var sess *receiveSession
	select {
	case sess = <-r.answer:
	case <-ctx.Done():
	case <-s.ctx.Done():
	}
	close(r.gone)
	if sess == nil || ctx.Err() != nil || s.ctx.Err() != nil {
		if sess != nil {
			sess.finish(errSenderGone, false)
		} else if ctx.Err() != nil && s.ev.Withdrawn != nil {
			s.ev.Withdrawn(req)
		}
		return nil, protocol.ErrDeclined
	}
	s.mu.Lock()
	if s.recv != nil {
		s.mu.Unlock()
		sess.finish(errors.New("busy with another transfer"), false)
		return nil, protocol.ErrBusy
	}
	s.recv = sess
	s.mu.Unlock()
	ids := make([]string, 0, len(o.Files))
	for _, f := range o.Files {
		ids = append(ids, f.ID)
	}
	return ids, nil
}

// Decline refuses the request.
func (r *Request) Decline() {
	if r.r == nil {
		return
	}
	select {
	case r.r.answer <- nil:
	case <-r.r.gone:
	}
}

// Accept accepts the request, saving the files into dir on fs (a file
// in a folder sent whole in that folder, created there), and returns
// once the transfer is over: every file received or failed, cancelled
// by the sender, or by prog.Cancelled. A name already taken in dir is
// numbered: "a (2).txt".
func (r *Request) Accept(fs vfs.FileSystem, dir string, prog *fileops.Progress) *fileops.Result {
	if r.r == nil {
		return &fileops.Result{}
	}
	s := r.r.s
	sess := &receiveSession{
		s: s, id: r.r.offer.SessionID, fs: fs, dir: dir, prog: prog,
		files: len(r.r.offer.Files), total: r.Size,
		tops: map[string]string{}, claimed: map[string]bool{},
		end: make(chan struct{}), last: time.Now(),
	}
	select {
	case r.r.answer <- sess:
	case <-r.r.gone:
		return &fileops.Result{Errors: []error{errSenderGone}}
	}
	return sess.wait()
}

// receiveSession is a transfer being received.
type receiveSession struct {
	s     *Service
	id    string
	fs    vfs.FileSystem
	dir   string
	prog  *fileops.Progress
	files int
	total int64

	received atomic.Int64 // bytes, of every file

	mu        sync.Mutex
	done      int // files received
	errs      []error
	tops      map[string]string // folder sent → its name in dir
	claimed   map[string]bool   // paths given to a file of the transfer
	last      time.Time         // the sender last sent something
	end       chan struct{}
	ended     bool
	endErr    error
	cancelled bool
}

// finish ends the session (the first call only): err, if not nil, is
// why it ended early.
func (r *receiveSession) finish(err error, cancelled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	r.ended, r.endErr, r.cancelled = true, err, cancelled
	close(r.end)
}

func (r *receiveSession) isEnded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ended
}

func (r *receiveSession) touch() {
	r.mu.Lock()
	r.last = time.Now()
	r.mu.Unlock()
}

// wait reports the session's progress until it ends.
func (r *receiveSession) wait() *fileops.Result {
	s := r.s
	tick := time.NewTicker(progressTick)
	defer tick.Stop()
	stop := func(err error, cancelled bool) {
		s.v2srv.EndSession(r.id)
		r.finish(err, cancelled)
	}
loop:
	for {
		select {
		case <-r.end:
			break loop
		case <-s.ctx.Done():
			stop(errors.New("LocalSend stopped"), false)
		case <-tick.C:
			r.reportBytes()
			r.mu.Lock()
			idle := time.Since(r.last)
			r.mu.Unlock()
			switch {
			case r.prog != nil && r.prog.Cancelled != nil && r.prog.Cancelled():
				stop(nil, true)
			case idle > idleTimeout:
				stop(errSenderIdle, false)
			}
		}
	}
	s.mu.Lock()
	if s.recv == r {
		s.recv = nil
	}
	s.mu.Unlock()
	s.changed()
	r.reportBytes()
	r.mu.Lock()
	defer r.mu.Unlock()
	res := &fileops.Result{Done: r.done, Errors: append([]error(nil), r.errs...), Cancelled: r.cancelled}
	if r.endErr != nil {
		res.Errors = append(res.Errors, r.endErr)
	}
	return res
}

func (r *receiveSession) reportBytes() {
	if r.prog == nil || r.prog.OnBytes == nil {
		return
	}
	r.mu.Lock()
	files := r.done + len(r.errs)
	r.mu.Unlock()
	r.prog.OnBytes(fileops.Bytes{Done: r.received.Load(), Total: r.total, Files: files, FilesTotal: r.files})
}

// fileDone records a file's end.
func (r *receiveSession) fileDone(name string, err error) {
	r.mu.Lock()
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("%s: %w", name, err))
	} else {
		r.done++
	}
	done, total := r.done+len(r.errs), r.files
	r.mu.Unlock()
	if r.prog != nil && r.prog.OnItem != nil {
		r.prog.OnItem(done, total, name, err)
	}
}

// receive saves a file of the session sessionID (see protocol.Handler).
func (s *Service) receive(ctx context.Context, sessionID string, f protocol.File, body io.Reader) error {
	s.mu.Lock()
	sess := s.recv
	s.mu.Unlock()
	if sess == nil || sess.id != sessionID || sess.isEnded() {
		return errNoSession
	}
	sess.touch()
	path, err := sess.target(f.Name)
	if err == nil {
		err = sess.save(ctx, path, f, body)
	}
	name := f.Name
	if path != "" {
		name = sess.fs.Base(path)
	}
	sess.fileDone(name, err)
	return err
}

// target returns where to save a file named name, creating its folders.
func (r *receiveSession) target(name string) (string, error) {
	segs := safePath(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	fs := r.fs
	if len(segs) > 1 {
		// A folder sent whole lands next to what's there, not into an
		// existing folder of the same name.
		top, ok := r.tops[segs[0]]
		if !ok {
			top = r.free(r.dir, segs[0])
			r.tops[segs[0]] = top
		}
		segs[0] = top
	}
	dir := r.dir
	for _, seg := range segs[:len(segs)-1] {
		dir = fs.Join(dir, seg)
		if e, err := fs.Stat(dir); err == nil {
			if !e.IsDir {
				return "", fmt.Errorf("%s is not a folder", seg)
			}
			continue
		}
		if err := fs.Mkdir(dir); err != nil {
			return "", err
		}
	}
	path := fs.Join(dir, r.free(dir, segs[len(segs)-1]))
	r.claimed[path] = true
	return path, nil
}

// free returns name, or a numbered one, free in dir; r.mu held.
func (r *receiveSession) free(dir, name string) string {
	taken := func(n string) bool {
		p := r.fs.Join(dir, n)
		if r.claimed[p] {
			return true
		}
		_, err := r.fs.Stat(p)
		return err == nil
	}
	if !taken(name) {
		return name
	}
	for n := 2; ; n++ {
		if c := numbered(name, n); !taken(c) {
			return c
		}
	}
}

// save writes body to path, removing what was written if it fails.
func (r *receiveSession) save(ctx context.Context, path string, f protocol.File, body io.Reader) error {
	var w io.WriteCloser
	var err error
	if sc, ok := r.fs.(vfs.SizedCreator); ok {
		w, err = sc.CreateSized(path, f.Size)
	} else {
		w, err = r.fs.Create(path)
	}
	if err != nil {
		return err
	}
	buf := make([]byte, 256<<10)
	var written int64
	for {
		if r.isEnded() {
			err = errors.New("transfer cancelled")
			break
		}
		if err = ctx.Err(); err != nil {
			break
		}
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				err = werr
				break
			}
			written += int64(n)
			r.received.Add(int64(n))
			r.touch()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			err = rerr
			break
		}
	}
	cerr := w.Close()
	if err == nil {
		err = cerr
	}
	if err != nil {
		r.received.Add(-written)
		r.fs.Remove(path)
		return err
	}
	if lp, ok := r.fs.(vfs.LocalPath); ok && !f.Modified.IsZero() {
		if local, ok := lp.LocalPath(path); ok {
			atime := f.Accessed
			if atime.IsZero() {
				atime = f.Modified
			}
			os.Chtimes(local, atime, f.Modified)
		}
	}
	return nil
}

// sessionEnded ends the session the protocol server ended.
func (s *Service) sessionEnded(sessionID string, cancelled bool) {
	s.mu.Lock()
	sess := s.recv
	s.mu.Unlock()
	if sess == nil || sess.id != sessionID {
		return
	}
	if cancelled {
		sess.finish(errSenderCancelled, false)
	} else {
		sess.finish(nil, false)
	}
}
