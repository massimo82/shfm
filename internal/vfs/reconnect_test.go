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
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// fakeConn is a connection the test can kill.
type fakeConn struct {
	id     int
	dead   atomic.Bool
	closed atomic.Bool
}

var errFakeClosed = errors.New("remote connection has closed")

func (c *fakeConn) op() error {
	if c.dead.Load() {
		return errFakeClosed
	}
	return nil
}

func newFakeSession(t *testing.T) (s *session[*fakeConn], dials *atomic.Int32) {
	t.Helper()
	old := sessionRedialInterval
	sessionRedialInterval = 0
	t.Cleanup(func() { sessionRedialInterval = old })
	dials = new(atomic.Int32)
	dial := func() (*fakeConn, error) { return &fakeConn{id: int(dials.Add(1))}, nil }
	first, _ := dial()
	s = newSession(first, dial,
		func(c *fakeConn) bool { return c.op() == nil },
		func(c *fakeConn) { c.closed.Store(true) })
	return s, dials
}

// A connection dropped by the server is replaced and the operation
// retried on the new one, closing the old one.
func TestSessionReconnectsDeadConnection(t *testing.T) {
	s, dials := newFakeSession(t)
	first, _ := s.get()
	first.dead.Store(true)

	var used *fakeConn
	err := do(s, func(c *fakeConn) error { used = c; return c.op() })
	if err != nil {
		t.Fatalf("do = %v, want success after reconnecting", err)
	}
	if used == first || dials.Load() != 2 {
		t.Fatalf("op ran on conn %d after %d dials, want a new connection", used.id, dials.Load())
	}
	deadline := time.Now().Add(time.Second)
	for !first.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !first.closed.Load() {
		t.Fatal("the dead connection was not closed")
	}
}

// Errors that are the server's answer, or that come from a connection that
// still answers the probe, don't replace the connection.
func TestSessionKeepsWorkingConnection(t *testing.T) {
	s, dials := newFakeSession(t)
	for _, err := range []error{os.ErrNotExist, io.EOF, errors.New("some protocol error")} {
		calls := 0
		got := do(s, func(c *fakeConn) error { calls++; return err })
		if got != err || calls != 1 {
			t.Fatalf("do(%v) = %v after %d calls, want the error back after 1", err, got, calls)
		}
	}
	if dials.Load() != 1 {
		t.Fatalf("%d dials, want none beyond the first", dials.Load()-1)
	}
}

// Two operations failing on the same dead connection reconnect only once.
func TestSessionReconnectsOnce(t *testing.T) {
	s, dials := newFakeSession(t)
	first, gen := s.get()
	first.dead.Store(true)
	if !s.failed(errFakeClosed, gen) || !s.failed(errFakeClosed, gen) {
		t.Fatal("failed = false, want a retry on the new connection")
	}
	if dials.Load() != 2 {
		t.Fatalf("%d dials, want 2", dials.Load())
	}
}

// Reconnections are rate-limited, and none happen after shutdown.
func TestSessionRedialLimits(t *testing.T) {
	s, dials := newFakeSession(t)
	sessionRedialInterval = time.Hour
	c, gen := s.get()
	c.dead.Store(true)
	if s.failed(errFakeClosed, gen) {
		t.Fatal("reconnected within the redial interval")
	}
	sessionRedialInterval = 0
	s.shutdown()
	if s.failed(errFakeClosed, gen) || dials.Load() != 1 {
		t.Fatal("reconnected after shutdown")
	}
	if !c.closed.Load() {
		t.Fatal("shutdown did not close the connection")
	}
}
