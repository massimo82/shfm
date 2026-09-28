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

package vfs

import (
	"io"
	"sync"
	"time"
)

// sessionRedialInterval is the minimum time between two reconnections of
// the same session, so a server that keeps dropping it isn't hammered.
var sessionRedialInterval = 5 * time.Second

// session holds a network backend's connection and replaces it with a new
// one when it turns out to be gone — typically a server or a NAT dropping
// it after hours of inactivity while a pane sits on the share — so that
// the next operation reconnects transparently instead of failing forever.
//
// Files and readers already opened on the old connection keep failing;
// only operations started through run/do/get after the reconnection use
// the new one.
type session[C any] struct {
	mu       sync.Mutex
	cur      C
	gen      uint64
	closed   bool
	lastDial time.Time

	dial func() (C, error)
	// alive probes whether c still works. A backend error only *may* mean
	// the connection is gone (see IsConnectionFailure): the probe keeps a
	// working connection — and the transfers running on it — from being
	// torn down over an error that was about the request itself.
	alive func(c C) bool
	close func(c C)
}

func newSession[C any](c C, dial func() (C, error), alive func(C) bool, closeFn func(C)) *session[C] {
	return &session[C]{cur: c, lastDial: time.Now(), dial: dial, alive: alive, close: closeFn}
}

// get returns the current connection and its generation.
func (s *session[C]) get() (C, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur, s.gen
}

// reconnect replaces the connection of generation gen if it's dead, and
// reports whether a newer connection is now available — possibly one
// another caller has already opened.
func (s *session[C]) reconnect(gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if s.gen != gen {
		return true
	}
	if time.Since(s.lastDial) < sessionRedialInterval || s.alive(s.cur) {
		return false
	}
	s.lastDial = time.Now()
	c, err := s.dial()
	if err != nil {
		return false
	}
	old := s.cur
	s.cur = c
	s.gen++
	// Closing a dead connection can wait for timeouts: don't hold the lock.
	go s.close(old)
	return true
}

// failed reports err, returned by an operation on the connection of
// generation gen, and whether that operation should be retried on a new
// connection. A bare io.EOF is the end of a file; wrapped, as in SFTP's
// "failed to send packet: EOF", it's the connection that ended.
func (s *session[C]) failed(err error, gen uint64) bool {
	return err != nil && err != io.EOF && IsConnectionFailure(err) && s.reconnect(gen)
}

// shutdown closes the current connection; later reconnections fail.
func (s *session[C]) shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.close(s.cur)
	}
}

// run runs op on the session's connection and, if it fails because the
// connection is gone, once more on a new one.
func run[C, T any](s *session[C], op func(c C) (T, error)) (T, error) {
	c, gen := s.get()
	v, err := op(c)
	if s.failed(err, gen) {
		c, _ = s.get()
		v, err = op(c)
	}
	return v, err
}

// do is run for an operation with no result besides the error.
func do[C any](s *session[C], op func(c C) error) error {
	_, err := run(s, func(c C) (struct{}, error) { return struct{}{}, op(c) })
	return err
}
