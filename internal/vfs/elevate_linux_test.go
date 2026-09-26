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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setPkexecPathForTest overrides the package's cached pkexec location for
// the duration of the test, restoring it afterwards — copying pkexecMu's
// guarded fields (plain string/bool) rather than the mutex itself, unlike
// an earlier version of this helper that copied pkexecOnce directly and
// tripped go vet's copylocks check (sync.Once embeds a noCopy sentinel).
// Never touches $PATH — mirrors how other tests in this codebase override
// an internal var to swap out an optional external tool (see
// internal/semantic/extract's pandocPath/sofficePath overrides).
func setPkexecPathForTest(t *testing.T, path string) {
	t.Helper()
	pkexecMu.Lock()
	savedPath, savedLocated := pkexecPath, pkexecLocated
	pkexecPath, pkexecLocated = path, true
	pkexecMu.Unlock()
	t.Cleanup(func() {
		pkexecMu.Lock()
		pkexecPath, pkexecLocated = savedPath, savedLocated
		pkexecMu.Unlock()
	})
}

// fakePkexec writes an executable shell script to dir that appends its own
// argv (one arg per line, "---" separated between invocations) to logPath,
// then exits with exitCode — standing in for the real pkexec so tests never
// need actual root or an interactive PolicyKit prompt.
func fakePkexec(t *testing.T, logPath string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pkexec.sh")
	content := fmt.Sprintf(`#!/bin/sh
{
  for a in "$@"; do printf '%%s\n' "$a"; done
  echo "---"
} >> %s
exit %d
`, shellQuote(logPath), exitCode)
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	setPkexecPathForTest(t, script)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// readLoggedArgs reads back what fakePkexec recorded for its most recent
// invocation (the last "---"-delimited block).
func readLoggedArgs(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	// Each invocation appends "<arg>\n"... "---\n", so splitting on "---\n"
	// leaves one trailing empty string after the final delimiter (trimming
	// first, before splitting, would instead eat the very "\n" the
	// delimiter depends on). Walk back to the last non-empty block, which
	// is the most recent invocation.
	blocks := strings.Split(string(data), "---\n")
	var last string
	for i := len(blocks) - 1; i >= 0; i-- {
		if blocks[i] != "" {
			last = blocks[i]
			break
		}
	}
	last = strings.TrimSuffix(last, "\n")
	if last == "" {
		return nil
	}
	// Every first attempt turns off pkexec's text prompt (see runElevated);
	// the tests care about the command it wraps.
	return strings.Split(strings.TrimPrefix(last, "--disable-internal-agent\n"), "\n")
}

func TestElevateIfPermissionErrorPassesThroughOtherErrors(t *testing.T) {
	called := false
	notPermission := errors.New("boom")
	got := elevateIfPermissionError(notPermission, func() error {
		called = true
		return nil
	})
	if called {
		t.Error("elevated fallback must not run for a non-permission error")
	}
	if !errors.Is(got, notPermission) && got.Error() != notPermission.Error() {
		t.Errorf("got %v, want the original error unchanged", got)
	}
}

func TestElevateIfPermissionErrorNilIsNoop(t *testing.T) {
	called := false
	if err := elevateIfPermissionError(nil, func() error { called = true; return nil }); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if called {
		t.Error("elevated fallback must not run when there's no error at all")
	}
}

func TestElevateIfPermissionErrorRunsFallbackForPermissionError(t *testing.T) {
	permErr := &os.PathError{Op: "chmod", Path: "/x", Err: os.ErrPermission}
	called := false
	err := elevateIfPermissionError(permErr, func() error {
		called = true
		return nil
	})
	if !called {
		t.Error("elevated fallback must run for a permission error")
	}
	if err != nil {
		t.Errorf("expected the fallback's own (nil) result, got %v", err)
	}
}

func TestRunElevatedSuccessPassesArgsThrough(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log.txt")
	fakePkexec(t, logPath, 0)

	if err := runElevated("chmod", "755", "--", "/some/path"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	got := readLoggedArgs(t, logPath)
	want := []string{"chmod", "755", "--", "/some/path"}
	if len(got) != len(want) {
		t.Fatalf("logged args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRunElevatedAuthDismissed(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log.txt")
	fakePkexec(t, logPath, 126)

	err := runElevated("rm", "-rf", "--", "/some/path")
	if err == nil {
		t.Fatal("expected an error for exit code 126")
	}
	if !strings.Contains(err.Error(), "dismissed") && !strings.Contains(err.Error(), "not authorized") {
		t.Errorf("expected a dismissed/not-authorized message, got %v", err)
	}
}

func TestRunElevatedCommandNotFound(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log.txt")
	fakePkexec(t, logPath, 127)

	err := runElevated("mv", "--", "/a", "/b")
	if err == nil {
		t.Fatal("expected an error for exit code 127")
	}
	if !strings.Contains(err.Error(), "pkexec") {
		t.Errorf("expected a pkexec-specific message, got %v", err)
	}
}

func TestRunElevatedGenericFailureSurfacesStderr(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-pkexec.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'chown: changing ownership: Operation not permitted' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	setPkexecPathForTest(t, script)

	err := runElevated("chown", "0:0", "--", "/some/path")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Operation not permitted") {
		t.Errorf("expected the wrapped command's stderr to surface, got %v", err)
	}
}

func TestRunElevatedPkexecNotInstalled(t *testing.T) {
	setPkexecPathForTest(t, "")

	err := runElevated("rm", "-rf", "--", "/some/path")
	if err == nil {
		t.Fatal("expected an error when pkexec isn't installed")
	}
	if !strings.Contains(err.Error(), "pkexec") {
		t.Errorf("expected a message naming pkexec, got %v", err)
	}
}

// --- real (no-root-needed) integration tests -------------------------------

// TestLocalFSChownFallsBackToElevation exercises the real LocalFS.Chown ->
// os.Chown -> EPERM -> elevated path end-to-end: a non-root user can never
// chown anything to a different uid (unlike chmod/rm/mv, no directory
// permission trick is needed — the kernel refuses this unconditionally for
// non-root), which makes it the one operation here reproducible for real
// without any elevated privileges at all.
func TestLocalFSChownFallsBackToElevation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chown wouldn't fail, nothing to elevate")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "log.txt")
	fakePkexec(t, logPath, 0)

	fs := NewLocalFS("Local", dir)
	otherUID := os.Geteuid() + 1 // definitely not "us" — chown to it requires real root
	if err := fs.Chown(path, otherUID, os.Getegid()); err != nil {
		t.Fatalf("expected the elevated fallback to report success, got %v", err)
	}
	got := readLoggedArgs(t, logPath)
	want := []string{"chown", fmt.Sprintf("%d:%d", otherUID, os.Getegid()), "--", path}
	if len(got) != len(want) {
		t.Fatalf("logged args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// unwritableDir returns a directory (owned by the current user, so no root
// is needed to create it) with its own write bit removed — even the owner
// can't add/remove/rename entries inside it without it, which is enough to
// make a plain os.Remove/os.Rename on something inside genuinely fail with
// EACCES, real permission semantics, no fixture needs to be root-owned.
func unwritableDir(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	dir := filepath.Join(parent, "locked")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) }) // let t.TempDir() clean up afterwards
	return dir
}

func TestLocalFSRemoveFallsBackToElevation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions wouldn't block us")
	}
	dir := unwritableDir(t)
	path := filepath.Join(dir, "f.txt")
	// Create the file before locking the directory down (can't create it
	// afterwards — that needs write access too).
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "log.txt")
	fakePkexec(t, logPath, 0)

	fs := NewLocalFS("Local", dir)
	if err := fs.Remove(path); err != nil {
		t.Fatalf("expected the elevated fallback to report success, got %v", err)
	}
	got := readLoggedArgs(t, logPath)
	want := []string{"rm", "-rf", "--", path}
	if len(got) != len(want) {
		t.Fatalf("logged args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLocalFSRenameFallsBackToElevation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions wouldn't block us")
	}
	dir := unwritableDir(t)
	oldPath := filepath.Join(dir, "old.txt")
	newPath := filepath.Join(dir, "new.txt")
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(t.TempDir(), "log.txt")
	fakePkexec(t, logPath, 0)

	fs := NewLocalFS("Local", dir)
	if err := fs.Rename(oldPath, newPath); err != nil {
		t.Fatalf("expected the elevated fallback to report success, got %v", err)
	}
	got := readLoggedArgs(t, logPath)
	want := []string{"mv", "--", oldPath, newPath}
	if len(got) != len(want) {
		t.Fatalf("logged args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestLocalFSOperationsSucceedNormallyWithoutElevation is a control: when
// there's no permission problem at all, these must behave exactly as before
// this file existed — no pkexec invocation, no behavioural change.
func TestLocalFSOperationsSucceedNormallyWithoutElevation(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log.txt")
	fakePkexec(t, logPath, 0) // would report success if (wrongly) invoked

	dir := t.TempDir()
	fs := NewLocalFS("Local", dir)

	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := fs.Chmod(path, 0o600); err != nil {
		t.Fatalf("plain Chmod should succeed: %v", err)
	}
	renamed := filepath.Join(dir, "g.txt")
	if err := fs.Rename(path, renamed); err != nil {
		t.Fatalf("plain Rename should succeed: %v", err)
	}
	if err := fs.Remove(renamed); err != nil {
		t.Fatalf("plain Remove should succeed: %v", err)
	}
	if got := readLoggedArgs(t, logPath); got != nil {
		t.Errorf("pkexec must not be invoked when there's no permission error, but got args %v", got)
	}
}

// noAgentPkexec installs a fake pkexec that behaves like the real one in a
// session without a PolicyKit agent: with --disable-internal-agent it
// fails with 127 and "No authentication agent found."; without it, it
// logs its argv (as fakePkexec does) and succeeds.
func noAgentPkexec(t *testing.T, logPath string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-pkexec.sh")
	content := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--disable-internal-agent" ]; then
  echo "Error executing command as another user: No authentication agent found." >&2
  exit 127
fi
{
  for a in "$@"; do printf '%%s\n' "$a"; done
  echo "---"
} >> %s
exit 0
`, shellQuote(logPath))
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	setPkexecPathForTest(t, script)
}

func setTerminalHandoffForTest(t *testing.T, f func(*exec.Cmd) error) {
	t.Helper()
	saved := TerminalHandoff
	TerminalHandoff = f
	t.Cleanup(func() { TerminalHandoff = saved })
}

func TestRunElevatedNoAgentHandsTerminalOver(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log.txt")
	noAgentPkexec(t, logPath)
	var handed []string
	setTerminalHandoffForTest(t, func(cmd *exec.Cmd) error {
		handed = cmd.Args[1:]
		return cmd.Run()
	})

	if err := runElevated("chown", "1000:1000", "--", "/some/path"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	want := []string{"chown", "1000:1000", "--", "/some/path"}
	if strings.Join(handed, " ") != strings.Join(want, " ") {
		t.Errorf("handed-over command = %v, want %v (without --disable-internal-agent)", handed, want)
	}
	if got := readLoggedArgs(t, logPath); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("logged args = %v, want %v", got, want)
	}
}

func TestRunElevatedNoAgentWithoutHandoffRunsDirectly(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log.txt")
	noAgentPkexec(t, logPath)
	setTerminalHandoffForTest(t, nil)

	if err := runElevated("chmod", "755", "--", "/some/path"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got := readLoggedArgs(t, logPath); len(got) == 0 || got[0] != "chmod" {
		t.Errorf("logged args = %v, want the chmod run", got)
	}
}

func TestRunElevatedWithAgentNeverHandsOver(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "log.txt")
	fakePkexec(t, logPath, 0)
	setTerminalHandoffForTest(t, func(*exec.Cmd) error {
		t.Error("the terminal must not be handed over when an agent prompts")
		return nil
	})
	if err := runElevated("rm", "-rf", "--", "/some/path"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestSetOwnerAndModeElevatesOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root never needs elevation")
	}
	logPath := filepath.Join(t.TempDir(), "log.txt")
	fakePkexec(t, logPath, 0)
	f := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := NewLocalFS("local", "/").SetOwnerAndMode(f, 0, 0, 0o600); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	data, _ := os.ReadFile(logPath)
	if n := strings.Count(string(data), "---\n"); n != 1 {
		t.Errorf("pkexec ran %d times, want once", n)
	}
	got := readLoggedArgs(t, logPath)
	want := []string{"sh", "-c", `chown "$1" -- "$3" && chmod "$2" -- "$3"`, "sh", "0:0", "600", f}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("logged args = %q, want %q", got, want)
	}
}
