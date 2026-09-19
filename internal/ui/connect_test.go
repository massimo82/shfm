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

package ui

import (
	"testing"
	"time"

	"shfm/internal/vfs"
)

// TestStartConnectNeverBlocks is the direct regression test for the bug
// report "selecting an MTP source freezes everything": it simulates a
// dial() that never returns on its own (exactly what a misbehaving USB
// device or an unreachable host looks like) and verifies that
// startConnect itself returns immediately regardless, and that the rest
// of the Model stays fully usable (other keys/actions keep working) while
// the connection attempt is still pending in the background.
func TestStartConnectNeverBlocks(t *testing.T) {
	m := newTestModel()

	release := make(chan struct{})
	dialStarted := make(chan struct{})

	start := time.Now()
	m.startConnect(m.active, "slow-device", func() (vfs.FileSystem, error) {
		close(dialStarted)
		<-release // never returns until the test says so
		return vfs.NewLocalFS("Local", "/"), nil
	}, nil, nil)
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Fatalf("startConnect took %v to return — it must return immediately, spawning the dial in the background", elapsed)
	}
	if m.dialog.Kind != DialogConnecting {
		t.Fatalf("expected a DialogConnecting placeholder right away, got %v", m.dialog.Kind)
	}

	select {
	case <-dialStarted:
	case <-time.After(time.Second):
		t.Fatal("the dial function never started running in the background")
	}

	// While the connection is still hanging, the rest of the UI must stay
	// fully responsive: dismissing the dialog (Esc — "send to background")
	// and continuing to navigate must work immediately, not wait for the
	// stuck dial() call.
	m.dialog = Dialog{}
	p := m.activePane()
	before := p.Cursor
	p.MoveCursor(1, 10)
	if p.Cursor == before && p.Len() > 1 {
		t.Error("pane navigation should keep working while a connection is pending")
	}
	m.setStatus("still alive")
	if m.status != "still alive" {
		t.Error("Model state updates should keep working while a connection is pending")
	}

	// Now let the stuck dial() finish and make sure the result is still
	// applied correctly once it does.
	close(release)
	select {
	case res := <-m.connectCh:
		m.handleConnectResult(res)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the (now released) connection result")
	}
	if m.panes[m.active].FS.Kind() != vfs.KindLocal {
		t.Error("expected the pane's source to have been replaced once the delayed connection finally completed")
	}
}

// TestStartConnectAppliesToOriginalPaneNotCurrentlyActiveOne verifies that
// a slow connection started on one pane is applied to THAT pane even if
// the user has since switched the active pane to the other one — a real
// consequence of making connections asynchronous that the fix must get
// right.
func TestStartConnectAppliesToOriginalPaneNotCurrentlyActiveOne(t *testing.T) {
	m := newTestModel()
	requestedPane := 0
	m.active = requestedPane

	release := make(chan struct{})
	m.startConnect(requestedPane, "slow-device", func() (vfs.FileSystem, error) {
		<-release
		return vfs.NewLocalFS("Local", "/"), nil
	}, nil, nil)

	// User switches away before the connection finishes.
	m.active = 1

	close(release)
	select {
	case res := <-m.connectCh:
		m.handleConnectResult(res)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the connection result")
	}

	if m.panes[requestedPane].FS.Kind() != vfs.KindLocal {
		t.Error("the connection result should apply to the pane that originally requested it")
	}
}
