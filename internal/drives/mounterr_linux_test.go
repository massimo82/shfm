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
//go:build linux

package drives

import (
	"errors"
	"strings"
	"syscall"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestUDisksErrorMissingService(t *testing.T) {
	for _, name := range []string{"org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner"} {
		err := udisksError(dbus.Error{Name: name, Body: []any{"The name is not activatable"}})
		if !errors.Is(err, ErrUDisks2Missing) {
			t.Errorf("%s: got %v, want ErrUDisks2Missing", name, err)
		}
	}
	other := dbus.Error{Name: "org.freedesktop.UDisks2.Error.Failed", Body: []any{"Error mounting"}}
	if err := udisksError(other); errors.Is(err, ErrUDisks2Missing) || !strings.Contains(err.Error(), "Error mounting") {
		t.Errorf("udisks2's own failure: got %v", err)
	}
}

func TestDirectMountFailure(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EPERM, syscall.EACCES} {
		err := directMountFailure("/dev/sdz1", errno)
		if err == nil || !strings.Contains(err.Error(), "administrator privileges") {
			t.Errorf("%v: got %v, want a privileges error", errno, err)
		}
	}
	for _, errno := range []syscall.Errno{syscall.EBUSY, syscall.ENOENT, syscall.ENXIO, syscall.ENOTBLK} {
		if directMountFailure("/dev/sdz1", errno) == nil {
			t.Errorf("%v: no other filesystem type would help, want an error", errno)
		}
	}
	// Not this filesystem: the next type is tried.
	for _, errno := range []syscall.Errno{syscall.ENODEV, syscall.EINVAL} {
		if err := directMountFailure("/dev/sdz1", errno); err != nil {
			t.Errorf("%v: got %v, want nil", errno, err)
		}
	}
}
