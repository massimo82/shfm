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

//go:build unix

package archive

import (
	"os/exec"
	"syscall"
)

// detach runs cmd in a new session, with no controlling terminal: a tool
// that would prompt on /dev/tty (bsdtar asking for an encrypted archive's
// passphrase) can't open it, so it fails instead of taking over shfm's
// terminal.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
