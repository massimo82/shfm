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

//go:build !linux

package vfs

import "errors"

// elevateIfPermissionError is a no-op outside Linux: pkexec/PolicyKit is a
// Linux-specific mechanism, so a permission error on the local filesystem
// is simply returned as-is, same as shfm's behaviour before elevate_linux.go
// existed.
func elevateIfPermissionError(err error, elevated func() error) error {
	return err
}

// runElevated only has to exist outside Linux, for LocalFS.Remove/Rename to
// compile: their calls are reached only when the Linux-only prediction in
// permcheck says elevation is needed (never true here, see permcheck_other.go)
// or through elevateIfPermissionError, a no-op above.
func runElevated(name string, args ...string) error {
	return errors.New("privilege elevation is only available on Linux")
}
