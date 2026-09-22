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
	"fmt"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"shfm/internal/applog"
	"shfm/internal/fileops"
	"shfm/internal/notify"
)

// TaskKind identifies the kind of file operation a background Task runs.
type TaskKind int

const (
	TaskCopy TaskKind = iota
	TaskMove
	TaskDelete
	TaskFormat
)

func (k TaskKind) String() string {
	switch k {
	case TaskCopy:
		return "Copy"
	case TaskMove:
		return "Move"
	case TaskDelete:
		return "Delete"
	case TaskFormat:
		return "Format"
	default:
		return "Task"
	}
}

// Task tracks the live progress of a background copy/move/delete. It keeps
// running (in its own goroutine) independently of whether any dialog is
// currently displaying it: closing the progress dialog simply stops
// *showing* it — that's what "send to background" means here — and
// reopening the task list (Ctrl+B) lets the user bring it back to the
// foreground at any time.
type Task struct {
	ID          int
	Kind        TaskKind
	Total       int
	Done        int
	ErrorCount  int
	CurrentName string
	Finished    bool
	Cancelled   bool
	LastError   error

	cancelFlag int32 // set atomically to 1 to request cancellation
}

func (t *Task) requestCancel()    { atomic.StoreInt32(&t.cancelFlag, 1) }
func (t *Task) isCancelled() bool { return atomic.LoadInt32(&t.cancelFlag) == 1 }

// Summary renders a short one-line status, used in the task list dialog and
// in the title bar's background-activity indicator.
func (t *Task) Summary() string {
	status := fmt.Sprintf("%d/%d", t.Done, t.Total)
	switch {
	case t.Cancelled:
		status = "cancelled"
	case t.Finished && t.ErrorCount > 0:
		status = fmt.Sprintf("done, %d error(s)", t.ErrorCount)
	case t.Finished:
		status = "done"
	}
	return fmt.Sprintf("%s — %s (%s)", t.Kind, status, t.CurrentName)
}

// taskMsg is sent on Model.taskCh from a background goroutine to report
// progress on, or the completion of, a Task; Update() applies it to the
// matching Task and re-issues waitForTaskMsg to keep listening.
type taskMsg struct {
	id        int
	done      int
	total     int
	name      string
	err       error
	finished  bool
	cancelled bool
	errCount  int
}

// waitForTaskMsg returns a tea.Cmd that blocks on the task channel and
// delivers the next update as a message — the standard bubbletea pattern
// for surfacing progress from independent goroutines.
func (m *Model) waitForTaskMsg() tea.Cmd {
	return func() tea.Msg {
		return <-m.taskCh
	}
}

func (m *Model) taskByID(id int) *Task {
	for _, t := range m.tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (m *Model) hasRunningTasks() bool {
	for _, t := range m.tasks {
		if !t.Finished {
			return true
		}
	}
	return false
}

// handleTaskMsg applies a taskMsg to the matching Task and, once a task
// finishes, refreshes both panes (a background operation may have changed
// either pane's current folder) — always safe here since Update() runs on
// bubbletea's single event-loop goroutine, unlike the task's own goroutine.
func (m *Model) handleTaskMsg(msg taskMsg) {
	t := m.taskByID(msg.id)
	if t == nil {
		return
	}
	if msg.finished {
		t.Finished = true
		t.Cancelled = msg.cancelled
		t.ErrorCount = msg.errCount
		m.panes[0].Load()
		m.panes[1].Load()
		m.notifyTaskFinished(t)
		return
	}
	t.Done, t.Total, t.CurrentName, t.LastError = msg.done, msg.total, msg.name, msg.err
	if msg.err != nil {
		t.ErrorCount++
	}
}

// startTask launches run in its own goroutine, wiring up a fileops.Progress
// that streams updates back through m.taskCh, and returns the new Task
// (already appended to m.tasks) so the caller can show it in a progress
// dialog immediately.
func (m *Model) startTask(kind TaskKind, total int, run func(prog *fileops.Progress) *fileops.Result) *Task {
	id := m.nextTaskID
	m.nextTaskID++
	t := &Task{ID: id, Kind: kind, Total: total}
	m.tasks = append(m.tasks, t)

	ch := m.taskCh
	go func() {
		prog := &fileops.Progress{
			OnItem: func(done, total int, name string, err error) {
				ch <- taskMsg{id: id, done: done, total: total, name: name, err: err}
			},
			Cancelled: t.isCancelled,
		}
		res := run(prog)
		ch <- taskMsg{id: id, finished: true, cancelled: res.Cancelled, errCount: len(res.Errors)}
	}()
	return t
}

// startSimpleTask is like startTask but for background operations that
// aren't itemized copy/move/delete batches (currently: formatting a
// device, a single atomic action) — reported as a single "item" that goes
// from 0/1 to 1/1.
func (m *Model) startSimpleTask(kind TaskKind, label string, run func() error) *Task {
	id := m.nextTaskID
	m.nextTaskID++
	t := &Task{ID: id, Kind: kind, Total: 1, CurrentName: label}
	m.tasks = append(m.tasks, t)

	ch := m.taskCh
	go func() {
		err := run()
		errCount := 0
		if err != nil {
			errCount = 1
			ch <- taskMsg{id: id, done: 0, total: 1, name: label, err: err}
		} else {
			ch <- taskMsg{id: id, done: 1, total: 1, name: label}
		}
		ch <- taskMsg{id: id, finished: true, errCount: errCount}
	}()
	return t
}

// isTaskForegrounded reports whether t's own progress dialog is the one
// currently on screen — i.e. the user is watching it live right now, as
// opposed to having sent it to the background (Esc) or never having it in
// front of them to begin with (see the Task doc comment for what
// "foreground"/"background" mean here). m.dialog is the single active
// dialog, so this is exactly the condition updateDialogKey's Esc case
// undoes when it backgrounds a task.
func (m *Model) isTaskForegrounded(t *Task) bool {
	return m.dialog.Kind == DialogProgress && m.dialog.TaskID == t.ID
}

// notifyTaskFinished posts a desktop notification (see internal/notify) for
// a finished task — success, error(s) or cancellation alike — but only if
// the task isn't currently foregrounded: a task the user is already
// watching finish needs no separate notification for the same event: it's
// the *backgrounded* case (sent to background then left running while the
// user moved on to something else) this exists for. Skipped entirely if
// the user opted out via config, and fire-and-forget otherwise: the D-Bus
// call happens in its own goroutine so a slow or absent session bus never
// blocks the UI's event loop, and any failure (typically just "no session
// bus", e.g. over SSH) is logged, not surfaced — a failed notification
// about a failure shouldn't become a second failure the user has to deal
// with.
func (m *Model) notifyTaskFinished(t *Task) {
	if (m.cfg != nil && !m.cfg.Notifications) || m.isTaskForegrounded(t) {
		return
	}
	urgency := notify.Normal
	status := "finished"
	switch {
	case t.Cancelled:
		status = "cancelled"
	case t.ErrorCount > 0:
		status = "finished with errors"
		urgency = notify.Critical
	}
	summary := fmt.Sprintf("shfm: %s %s", t.Kind, status)
	body := t.Summary()
	go func() {
		if err := notify.Send(summary, body, urgency); err != nil {
			applog.Debug("desktop notification failed", "error", err)
		}
	}()
}
