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

import "testing"

// Eject refuses the system disk before unmounting anything on it.
func TestEjectRefusesSystemDisk(t *testing.T) {
	sys, err := SystemDiskName()
	if err != nil {
		t.Skip("cannot determine the system disk here")
	}
	if off, err := Eject("/dev/" + sys); err == nil || off {
		t.Fatalf("Eject(system disk) = %v, %v; want a refusal", off, err)
	}
}
