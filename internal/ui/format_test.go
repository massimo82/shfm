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

	"shfm/internal/config"
	"shfm/internal/drives"
)

func newTestModel() *Model {
	return New(config.Default(), config.DefaultKeyMap())
}

func TestFormatDialogFlow(t *testing.T) {
	m := newTestModel()

	dev := drives.RemovableDevice{Path: "/dev/sdz1", Name: "sdz1", SizeBytes: 16 << 30}
	m.openFormatChoose(dev)

	if m.dialog.Kind != DialogFormatChoose {
		t.Fatalf("expected DialogFormatChoose, got %v", m.dialog.Kind)
	}
	if len(m.dialog.Items) != len(drives.FormatChoices) {
		t.Fatalf("expected %d filesystem choices, got %d", len(drives.FormatChoices), len(m.dialog.Items))
	}

	// Pick the second choice (FAT32) and confirm -> should move to Confirm1
	// with a RED warning message and the chosen filesystem carried over.
	m.dialog.ItemIdx = 1
	m.confirmDialog()
	if m.dialog.Kind != DialogFormatConfirm1 {
		t.Fatalf("expected DialogFormatConfirm1, got %v", m.dialog.Kind)
	}
	if m.dialog.FormatFSType != drives.FormatChoices[1].Type {
		t.Fatalf("FormatFSType = %v, want %v", m.dialog.FormatFSType, drives.FormatChoices[1].Type)
	}
	if m.dialog.FormatDevice.Path != dev.Path {
		t.Fatalf("FormatDevice.Path = %q, want %q", m.dialog.FormatDevice.Path, dev.Path)
	}

	// Confirming step 1 -> DialogFormatConfirm2, requiring a typed "YES".
	m.confirmDialog()
	if m.dialog.Kind != DialogFormatConfirm2 {
		t.Fatalf("expected DialogFormatConfirm2, got %v", m.dialog.Kind)
	}
	if len(m.dialog.Inputs) != 1 {
		t.Fatalf("expected exactly one text input for the YES confirmation, got %d", len(m.dialog.Inputs))
	}

	// Wrong / incomplete text must NOT proceed: the dialog stays open so
	// the destructive action can't be triggered by a stray Enter press.
	m.dialog.Inputs[0].SetValue("yes") // wrong case
	m.confirmDialog()
	if m.dialog.Kind != DialogFormatConfirm2 {
		t.Fatalf("wrong confirmation text should NOT proceed past DialogFormatConfirm2, got %v", m.dialog.Kind)
	}

	m.dialog.Inputs[0].SetValue("Y")
	m.confirmDialog()
	if m.dialog.Kind != DialogFormatConfirm2 {
		t.Fatalf("incomplete confirmation text should NOT proceed past DialogFormatConfirm2, got %v", m.dialog.Kind)
	}

	// The exact literal "YES" proceeds: a background Task is started and a
	// progress dialog opens immediately.
	m.dialog.Inputs[0].SetValue("YES")
	m.confirmDialog()
	if m.dialog.Kind != DialogProgress {
		t.Fatalf("expected DialogProgress after typing YES, got %v", m.dialog.Kind)
	}
	if len(m.tasks) != 1 {
		t.Fatalf("expected exactly one background task to have been started, got %d", len(m.tasks))
	}
	if m.tasks[0].Kind != TaskFormat {
		t.Fatalf("task kind = %v, want TaskFormat", m.tasks[0].Kind)
	}

	// Drain the task's message(s) until completion (it will fail since
	// there's no real device / no udisks2 available in this environment —
	// that's expected and fine: we're only verifying the UI state
	// machine, not that formatting a nonexistent device actually
	// succeeds; on error, startSimpleTask sends an item-error message
	// followed by a finished message).
	for {
		msg := <-m.taskCh
		m.handleTaskMsg(msg)
		if msg.finished {
			break
		}
	}
	if !m.tasks[0].Finished {
		t.Fatalf("expected the task to be marked finished after draining its completion message")
	}
}

func TestFormatMenuHidesSystemDisk(t *testing.T) {
	sysDisk, err := drives.SystemDiskName()
	if err != nil {
		t.Skip("cannot determine system disk in this environment")
	}
	if !drives.IsSystemDisk("/dev/" + sysDisk) {
		t.Errorf("IsSystemDisk should report true for the system disk %q", sysDisk)
	}
	if drives.IsSystemDisk("/dev/sdz1") {
		t.Errorf("IsSystemDisk should report false for an unrelated device")
	}
}
