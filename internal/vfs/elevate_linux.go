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

package vfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// pkexec is located once, lazily, and cached — the same pattern as every
// other optional external tool shfm reaches for (see internal/semantic/
// extract's pandoc/LibreOffice/xz/7z detection). Without it, a permission
// error on the local filesystem is simply returned as-is, same as shfm's
// behaviour before this file existed.
//
// A plain mutex+bool guards this rather than sync.Once: tests need to
// swap pkexecPath out for a fake and back again, and copying a sync.Once
// (as saving/restoring one for that would require) is unsafe — go vet
// flags it outright (sync.Once embeds a noCopy sentinel for exactly this
// reason).
var (
	pkexecMu      sync.Mutex
	pkexecPath    string
	pkexecLocated bool
)

func locatePkexec() {
	pkexecMu.Lock()
	defer pkexecMu.Unlock()
	if pkexecLocated {
		return
	}
	defer func() { pkexecLocated = true }()
	if p, err := exec.LookPath("pkexec"); err == nil {
		pkexecPath = p
	}
}

// elevateTimeout bounds how long an elevated command is allowed to run —
// generous, since it covers both a human typing a password into
// PolicyKit's own prompt and a potentially large recursive rm/mv once
// authenticated.
const elevateTimeout = 5 * time.Minute

// runElevated runs name with args as root via pkexec. pkexec handles its
// own authentication — shfm never sees or touches the password. A
// graphical PolicyKit agent, when the session has one, prompts in its own
// window. Without one, pkexec would fall back to a text prompt written
// straight onto the terminal, over the TUI: so the first attempt disables
// that fallback, and only if no agent is found is pkexec run again with
// the terminal handed over to it (TerminalHandoff), the TUI stepping aside
// until it exits.
func runElevated(name string, args ...string) error {
	locatePkexec()
	if pkexecPath == "" {
		return fmt.Errorf("elevation needs pkexec, which isn't installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), elevateTimeout)
	defer cancel()
	argv := append([]string{name}, args...)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, pkexecPath, append([]string{"--disable-internal-agent"}, argv...)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	err := cmd.Run()
	if isNoAgent(err, stderr.String()) {
		stderr.Reset()
		cmd = exec.CommandContext(ctx, pkexecPath, argv...)
		cmd.Stdout = io.Discard
		if TerminalHandoff != nil {
			cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
			err = TerminalHandoff(cmd)
		} else {
			cmd.Stderr = &stderr
			err = cmd.Run()
		}
	}
	if err == nil {
		return nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("elevated %s timed out", name)
	}
	// pkexec itself exits 126 (auth dismissed) or 127 (not authorized,
	// or couldn't even run the command) — distinguish those from the wrapped
	// command's own failure, since stderr for the former is pkexec's own
	// (often unhelpful) message, not the underlying tool's.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case 126:
			return errors.New("authentication dismissed or not authorized")
		case 127:
			// 127 covers every failure of pkexec's own (not authorized,
			// wrong password, agent registration, missing command): its
			// message is the only way to tell them apart.
			if msg := strings.Join(strings.Fields(stderr.String()), " "); msg != "" {
				return errors.New("pkexec: " + msg)
			}
			return errors.New("pkexec: not authorized, or the command couldn't be run")
		}
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("%s: %s", name, msg)
}

// isNoAgent reports whether a pkexec run with --disable-internal-agent
// failed only because the session has no PolicyKit authentication agent.
func isNoAgent(err error, stderr string) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 127 &&
		strings.Contains(stderr, "No authentication agent")
}

// elevateIfPermissionError retries via elevated (an operation that redoes
// the same thing through pkexec-wrapped coreutils) when err is a
// permission error (EACCES/EPERM) — i.e. the operation on path plausibly
// failed only because shfm itself isn't running as root, which pkexec can
// fix, as opposed to some other reason retrying wouldn't help with (a
// missing path, a read-only filesystem, ...). Anything else is returned
// unchanged.
func elevateIfPermissionError(err error, elevated func() error) error {
	if err == nil || !os.IsPermission(err) {
		return err
	}
	return elevated()
}
