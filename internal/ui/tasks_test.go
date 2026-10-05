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
	"errors"
	"strings"
	"testing"
	"time"

	"shfm/internal/fileops"
)

func TestQuitGuardBlocksWithRunningTask(t *testing.T) {
	m := newTestModel()

	// Start a task that we control the completion of manually, so it stays
	// "running" for the purposes of this test.
	m.startTask(TaskCopy, 1, func(prog *fileops.Progress) *fileops.Result {
		<-make(chan struct{}) // never returns on its own
		return &fileops.Result{}
	})

	m.requestQuit()
	if m.quitting {
		t.Fatal("requestQuit should NOT set quitting while a task is still running")
	}
	if m.dialog.Kind != DialogConfirmQuit {
		t.Fatalf("expected DialogConfirmQuit to be opened, got %v", m.dialog.Kind)
	}

	// Confirming the warning does quit.
	m.confirmDialog()
	if !m.quitting {
		t.Fatal("confirming DialogConfirmQuit should set quitting=true")
	}
}

func TestQuitGuardAllowsImmediateQuitWithNoTasks(t *testing.T) {
	m := newTestModel()
	m.requestQuit()
	if !m.quitting {
		t.Fatal("requestQuit should quit immediately when there are no background tasks")
	}
	if m.dialog.Kind != DialogNone {
		t.Fatalf("no dialog should be opened, got %v", m.dialog.Kind)
	}
}

func TestQuitGuardAllowsQuitAfterTaskFinishes(t *testing.T) {
	m := newTestModel()
	done := make(chan struct{})
	m.startTask(TaskCopy, 1, func(prog *fileops.Progress) *fileops.Result {
		<-done
		return &fileops.Result{Done: 1}
	})
	close(done)
	// Drain until finished.
	for {
		msg := <-m.taskCh
		m.handleTaskMsg(msg)
		if msg.finished {
			break
		}
	}
	m.requestQuit()
	if !m.quitting {
		t.Fatal("requestQuit should quit immediately once the only task has finished")
	}
}

func TestTaskSummaryReflectsState(t *testing.T) {
	task := &Task{ID: 1, Kind: TaskCopy, Total: 3}
	if s := task.Summary(); s == "" {
		t.Fatal("Summary() should never be empty")
	}
	task.Done = 3
	task.Finished = true
	s := task.Summary()
	if !containsSubstring(s, "done") {
		t.Errorf("finished task summary should mention it's done, got %q", s)
	}

	task2 := &Task{ID: 2, Kind: TaskDelete, Total: 2, Finished: true, ErrorCount: 1}
	if s := task2.Summary(); !containsSubstring(s, "error") {
		t.Errorf("summary with errors should mention them, got %q", s)
	}

	task3 := &Task{ID: 3, Kind: TaskCopy, Total: 1, Finished: true, ErrorCount: 1,
		CurrentName: "rsync (delta transfer)", LastError: errors.New("mkdir /x: permission denied")}
	if s := task3.Summary(); !containsSubstring(s, "permission denied") {
		t.Errorf("summary with errors should show the last error, got %q", s)
	}
}

func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestByteProgress: a copying task shows its bytes in human-readable
// form, out of the total, with its speed, the time left and its files.
func TestByteProgress(t *testing.T) {
	task := &Task{Kind: TaskCopy, Total: 1}
	start := time.Now()
	const mib = 1 << 20
	task.updateBytes(fileops.Bytes{Done: 0, Total: -1, Files: 0, FilesTotal: -1}, start)
	if out := renderByteProgress(task); !strings.Contains(out, "0B of ?") || !strings.Contains(out, "0/? files") {
		t.Errorf("while measuring:\n%s", out)
	}
	task.updateBytes(fileops.Bytes{Done: 10 * mib, Total: 40 * mib, Files: 2, FilesTotal: 5}, start.Add(time.Second))
	out := renderByteProgress(task)
	for _, want := range []string{"25%", "10.0M of 40.0M", "10.0M/s", "3s left", "2/5 files"} {
		if !strings.Contains(out, want) {
			t.Errorf("progress lacks %q:\n%s", want, out)
		}
	}
	if s := task.Summary(); !strings.Contains(s, "25%, 3s left") {
		t.Errorf("Summary() = %q", s)
	}

	// A move made of renames has nothing to show in bytes.
	renames := &Task{Kind: TaskMove, Total: 2, Done: 1}
	renames.updateBytes(fileops.Bytes{}, start)
	if renames.showsBytes() || !strings.Contains(renames.Summary(), "1/2") {
		t.Errorf("a move by renames: showsBytes %v, Summary() %q", renames.showsBytes(), renames.Summary())
	}
}

func TestFormatETA(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second:              "45s",
		3*time.Minute + 5*time.Second: "3m 05s",
		2*time.Hour + 10*time.Minute:  "2h 10m",
		1500 * time.Millisecond:       "2s",
	} {
		if got := formatETA(d); got != want {
			t.Errorf("formatETA(%v) = %q, want %q", d, got, want)
		}
	}
}
