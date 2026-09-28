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

// Package idle tells a D-Bus activated service when it has been unused
// long enough to exit: the bus starts it again on the next call.
package idle

import (
	"sync"
	"time"
)

// Tracker counts the requests in progress and when the last one ended.
type Tracker struct {
	mu     sync.Mutex
	active int
	last   time.Time
}

// New returns a Tracker whose idle time starts now.
func New() *Tracker { return &Tracker{last: time.Now()} }

// Begin marks a request in progress; call the returned func when it ends.
func (t *Tracker) Begin() (end func()) {
	t.mu.Lock()
	t.active++
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			t.active--
			t.last = time.Now()
			t.mu.Unlock()
		})
	}
}

// Idle reports whether no request is in progress and none ended in the
// last d.
func (t *Tracker) Idle(d time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active == 0 && time.Since(t.last) >= d
}

// Wait returns once the tracker has been Idle for d, checking every tick,
// or once stop is closed (the bus connection is gone).
func (t *Tracker) Wait(d, tick time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for !t.Idle(d) {
		select {
		case <-ticker.C:
		case <-stop:
			return
		}
	}
}
