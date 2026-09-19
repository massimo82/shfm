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
	tea "github.com/charmbracelet/bubbletea"

	"shfm/internal/vfs"
)

// connectResultMsg carries the outcome of an asynchronous source connection
// attempt (MTP/SMB/NFS/SFTP), all of which involve blocking USB or network
// I/O that must NEVER run directly on bubbletea's single event-loop
// goroutine: doing so freezes the entire UI for as long as the attempt
// takes — which, for a misbehaving USB device or an unreachable host, can
// be tens of seconds or more (each individual USB bulk transfer alone has
// a generous timeout, and a connection involves several of them in
// sequence). This is exactly the bug that made selecting an MTP source
// lock up the whole program.
type connectResultMsg struct {
	requestID int
	paneIndex int
	label     string
	fs        vfs.FileSystem
	err       error
	onSuccess func()
	onError   func(errText string)
}

// waitForConnectMsg returns a tea.Cmd that blocks on the connection-result
// channel and delivers the next one as a message — the same long-poll
// pattern used for background file-operation tasks and folder-size scans.
func (m *Model) waitForConnectMsg() tea.Cmd {
	return func() tea.Msg {
		return <-m.connectCh
	}
}

// startConnect runs dial() in its own goroutine and immediately shows a
// "Connecting…" dialog; the UI stays fully responsive for the entire
// duration of the attempt, no matter how long it takes or whether it ever
// completes. Closing the dialog (Esc) only stops watching it — exactly
// like a background file-operation task — the connection attempt keeps
// running regardless and is applied to paneIndex whenever it finishes.
//
// onSuccess (optional) runs after a successful connection has replaced the
// pane's source, e.g. to save the connection details for next time.
// onError (optional) runs instead of the default status-bar error message
// on failure, e.g. to reopen a connection form with the error shown inline
// and the previously typed values preserved.
func (m *Model) startConnect(paneIndex int, label string, dial func() (vfs.FileSystem, error), onSuccess func(), onError func(string)) {
	id := m.nextConnectID
	m.nextConnectID++
	ch := m.connectCh
	go func() {
		fs, err := dial()
		ch <- connectResultMsg{
			requestID: id, paneIndex: paneIndex, label: label,
			fs: fs, err: err, onSuccess: onSuccess, onError: onError,
		}
	}()
	m.dialog = Dialog{
		Kind: DialogConnecting, Title: "Connecting",
		Message: "Connecting to " + label + "…", ConnectRequestID: id,
	}
}

// handleConnectResult applies a connectResultMsg: replaces the target
// pane's source on success (running onSuccess afterwards, if given) or
// reports the failure (via onError if given, otherwise the status bar) —
// and, in either case, only touches the "Connecting…" dialog if it's still
// showing THIS specific attempt (the user may have long since dismissed it
// or started a different one).
func (m *Model) handleConnectResult(msg connectResultMsg) {
	showingThis := m.dialog.Kind == DialogConnecting && m.dialog.ConnectRequestID == msg.requestID

	if msg.err != nil {
		if showingThis {
			m.dialog = Dialog{}
		}
		if msg.onError != nil {
			msg.onError(msg.err.Error())
		} else {
			m.setError("Connecting to %s failed: %v", msg.label, msg.err)
		}
		return
	}

	m.replaceFS(msg.paneIndex, msg.fs, msg.fs.Root())
	if showingThis {
		m.dialog = Dialog{}
	}
	if msg.onSuccess != nil {
		msg.onSuccess()
	}
	m.setStatus("Connected to %s", msg.label)
}
