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

// Package toolpath locates the external programs shfm runs. PATH alone
// isn't enough: started by D-Bus activation (as the portal's file dialog)
// or from a desktop launcher, shfm may get a minimal PATH, without the
// folders a user's shell adds (~/.local/bin, NixOS profiles, /snap/bin...).
package toolpath

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// lookPath and searchDirs are variables so tests can fake PATH and the
// known folders.
var (
	lookPath   = exec.LookPath
	searchDirs = knownDirs
)

// Find returns the path of the program name: looked up in PATH first,
// then in the folders programs are commonly installed in. A name
// containing a slash is a path, only checked. The error is PATH's when
// the program is nowhere.
func Find(name string) (string, error) {
	p, err := lookPath(name)
	if err == nil || strings.Contains(name, "/") {
		return p, err
	}
	for _, dir := range searchDirs() {
		if p, ferr := lookPath(filepath.Join(dir, name)); ferr == nil {
			return p, nil
		}
	}
	return "", err
}

// knownDirs lists the folders searched after PATH: the system's first,
// then the package managers' that live outside it, the user's own last.
func knownDirs() []string {
	dirs := []string{
		"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin",
		// NixOS: setuid wrappers, then the system and default profiles.
		"/run/wrappers/bin", "/run/current-system/sw/bin", "/nix/var/nix/profiles/default/bin",
		"/snap/bin",
		"/home/linuxbrew/.linuxbrew/bin",
	}
	if user := os.Getenv("USER"); user != "" {
		dirs = append(dirs, "/etc/profiles/per-user/"+user+"/bin")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs,
			filepath.Join(home, ".nix-profile", "bin"),
			filepath.Join(home, ".linuxbrew", "bin"),
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
		)
	}
	return dirs
}

// FindFile returns the first of paths that is an executable file, "" if
// none is: for a program with no fixed place and not meant to be in any
// PATH (a helper under a distribution-specific libexec folder).
func FindFile(paths ...string) string {
	for _, p := range paths {
		if found, err := lookPath(p); err == nil {
			return found
		}
	}
	return ""
}
