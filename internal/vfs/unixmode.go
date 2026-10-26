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
package vfs

import (
	"fmt"
	"os"
)

// Unix mode bits, as stat(2) and chmod(2) spell them. os.FileMode keeps the
// setuid/setgid/sticky bits elsewhere (ModeSetuid...), so a mode shown to
// the user as octal, or handed to chmod(1), must go through UnixMode.
const (
	unixTypeMask = 0o170000
	unixDir      = 0o040000
	unixSymlink  = 0o120000
	unixFIFO     = 0o010000
	unixSocket   = 0o140000
	unixCharDev  = 0o020000
	unixBlockDev = 0o060000
	unixSetuid   = 0o4000
	unixSetgid   = 0o2000
	unixSticky   = 0o1000
)

// UnixMode is m's permission bits in octal Unix form, setuid/setgid/sticky
// included (e.g. 02755): what chmod(1) takes and ls -l shows.
func UnixMode(m os.FileMode) uint32 {
	u := uint32(m.Perm())
	if m&os.ModeSetuid != 0 {
		u |= unixSetuid
	}
	if m&os.ModeSetgid != 0 {
		u |= unixSetgid
	}
	if m&os.ModeSticky != 0 {
		u |= unixSticky
	}
	return u
}

// FromUnixMode turns a raw Unix mode (st_mode, or an octal mode typed by
// the user) into an os.FileMode, file type and special bits included.
func FromUnixMode(raw uint32) os.FileMode {
	m := os.FileMode(raw & 0o777)
	switch raw & unixTypeMask {
	case unixDir:
		m |= os.ModeDir
	case unixSymlink:
		m |= os.ModeSymlink
	case unixFIFO:
		m |= os.ModeNamedPipe
	case unixSocket:
		m |= os.ModeSocket
	case unixCharDev:
		m |= os.ModeDevice | os.ModeCharDevice
	case unixBlockDev:
		m |= os.ModeDevice
	}
	if raw&unixSetuid != 0 {
		m |= os.ModeSetuid
	}
	if raw&unixSetgid != 0 {
		m |= os.ModeSetgid
	}
	if raw&unixSticky != 0 {
		m |= os.ModeSticky
	}
	return m
}

// chmodArg is mode as chmod(1)'s numeric argument. Five digits, leading
// zero included: GNU chmod leaves a directory's setuid/setgid bits alone
// unless the mode spells them out that way, and a restore must clear them
// as faithfully as it sets them.
func chmodArg(mode os.FileMode) string {
	return fmt.Sprintf("%05o", UnixMode(mode))
}
