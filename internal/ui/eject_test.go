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

	"shfm/internal/drives"
)

// fakeEject replaces ejectDrive for one test, recording the disks asked.
func fakeEject(t *testing.T, poweredOff bool, err error) *[]string {
	t.Helper()
	var asked []string
	old := ejectDrive
	ejectDrive = func(disk string) (bool, error) {
		asked = append(asked, disk)
		return poweredOff, err
	}
	t.Cleanup(func() { ejectDrive = old })
	return &asked
}

func drainTask(m *Model) {
	for {
		msg := <-m.taskCh
		m.handleTaskMsg(msg)
		if msg.finished {
			return
		}
	}
}

func TestSourceMenuOneEjectPerDisk(t *testing.T) {
	old := listRemovableDrives
	listRemovableDrives = func() ([]drives.RemovableDevice, error) {
		return []drives.RemovableDevice{
			{Name: "sdy1", Path: "/dev/sdy1", SizeBytes: 8 << 30, Mounted: true, MountPoint: "/run/media/u/A", FSType: "vfat"},
			{Name: "sdy2", Path: "/dev/sdy2", SizeBytes: 8 << 30},
			{Name: "sdz", Path: "/dev/sdz", SizeBytes: 4 << 30, Mounted: true, MountPoint: "/run/media/u/B", FSType: "exfat"},
		}, nil
	}
	t.Cleanup(func() { listRemovableDrives = old })

	m := newTestModel()
	m.openSourceMenu()
	var ejects []string
	lastSdy := -1
	for i, e := range m.sourceMenuEntries {
		if e.kind == "eject-request" {
			ejects = append(ejects, e.label)
			if strings.Contains(e.label, "/dev/sdy") && i < lastSdy {
				t.Errorf("Eject for /dev/sdy before its partition %d", lastSdy)
			}
		} else if strings.Contains(e.label, "/dev/sdy") {
			lastSdy = i
		}
	}
	if len(ejects) != 2 || !strings.Contains(ejects[0], "Eject /dev/sdy ") || !strings.Contains(ejects[1], "Eject /dev/sdz ") {
		t.Errorf("eject entries %q, want one for /dev/sdy then /dev/sdz", ejects)
	}
}

func TestEjectDisk(t *testing.T) {
	asked := fakeEject(t, true, nil)
	m := newTestModel()
	m.ejectDisk(drives.RemovableDevice{Name: "sdz1", Path: "/dev/sdz1"})
	if m.dialog.Kind != DialogProgress || len(m.tasks) != 1 || m.tasks[0].Kind != TaskEject {
		t.Fatalf("dialog %v, tasks %d: want an Eject task in a progress dialog", m.dialog.Kind, len(m.tasks))
	}
	drainTask(m)
	if len(*asked) != 1 || (*asked)[0] != "/dev/sdz" {
		t.Errorf("ejected %q, want the whole disk /dev/sdz", *asked)
	}
	if got := m.tasks[0].CurrentName; got != "/dev/sdz: safe to remove" {
		t.Errorf("task says %q", got)
	}
}

func TestEjectDiskUnmountedOnly(t *testing.T) {
	fakeEject(t, false, nil)
	m := newTestModel()
	m.ejectDisk(drives.RemovableDevice{Name: "sdz", Path: "/dev/sdz"})
	drainTask(m)
	if got := m.tasks[0].CurrentName; got != "/dev/sdz unmounted: safe to remove" {
		t.Errorf("task says %q", got)
	}
}

func TestEjectDiskFailure(t *testing.T) {
	fakeEject(t, false, errors.New("/dev/sdz1 in use, can't unmount: busy"))
	m := newTestModel()
	m.ejectDisk(drives.RemovableDevice{Name: "sdz1", Path: "/dev/sdz1"})
	drainTask(m)
	if tk := m.tasks[0]; tk.ErrorCount != 1 || strings.Contains(tk.CurrentName, "safe to remove") {
		t.Errorf("failed eject: %d errors, says %q", tk.ErrorCount, tk.CurrentName)
	}
}

func TestEjectRefusedWhileTasksRun(t *testing.T) {
	asked := fakeEject(t, true, nil)
	m := newTestModel()
	m.tasks = append(m.tasks, &Task{ID: 99, Kind: TaskCopy, Total: 1})
	m.ejectDisk(drives.RemovableDevice{Name: "sdz1", Path: "/dev/sdz1"})
	if len(*asked) != 0 || len(m.tasks) != 1 {
		t.Errorf("eject started with a task running")
	}
}
