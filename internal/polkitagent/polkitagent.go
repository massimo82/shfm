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

// Package polkitagent is a PolicyKit authentication agent for shfm's own
// process, so that pkexec (which shfm runs to act as root) asks for the
// password in a TUI dialog: no graphical agent is needed, and pkexec's
// own text prompt — written over the TUI, and broken with polkit's
// socket-activated helper — is never used. It needs nothing beyond
// polkit itself: D-Bus to polkitd, and the line protocol of
// polkit-agent-helper-1, which checks the password with PAM.
package polkitagent

import "context"

// Prompt is one question for the user.
type Prompt struct {
	Message string // what polkit says the authentication is for
	User    string // whose password is asked
	Text    string // PAM's prompt, e.g. "Password: "
	Echo    bool   // show what's typed (not a password)
	Notice  string // PAM's messages since the last prompt, or why the previous attempt failed
}

// Prompter asks the user and returns the answer, or false when the user
// dismissed the prompt or ctx was cancelled (polkit withdrew the request).
type Prompter func(ctx context.Context, p Prompt) (string, bool)
