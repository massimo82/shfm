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

import "os/exec"

// TerminalHandoff, when set (by the TUI), runs cmd in the foreground of
// shfm's own terminal — the TUI releases it until cmd exits, then
// repaints — and returns cmd's error. It lets an elevated command ask for
// a password in the terminal when no graphical PolicyKit agent is
// running. It must not be called from the TUI's event loop, which it
// waits on. When nil, cmd simply runs over whatever is on the terminal.
var TerminalHandoff func(cmd *exec.Cmd) error
