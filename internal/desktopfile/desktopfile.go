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

// Package desktopfile installs a .desktop launcher entry for shfm on first
// run, so it shows up in the desktop environment's application menu,
// without requiring a separate manual installation step.
package desktopfile

import (
	"fmt"
	"os"
	"path/filepath"
)

const fileName = "shfm.desktop"

const template = `[Desktop Entry]
Type=Application
Name=shfm
GenericName=Shell File Manager
Comment=Terminal-based file manager with SMB/NFS/SFTP/MTP support
Exec=%s
Terminal=true
Icon=system-file-manager
Categories=System;Utility;FileTools;
Keywords=file;manager;terminal;shell;sftp;smb;nfs;mtp;
`

// systemDir, userDir and getUID are declared as vars/funcs (rather than
// direct os.* calls) so tests can override them without depending on the
// actual process UID or real filesystem locations.
var systemDir = "/usr/share/applications"

var getUID = os.Getuid

func userDir() (string, error) {
	if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
		return filepath.Join(dataHome, "applications"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "applications"), nil
}

// EnsureInstalled installs shfm.desktop on first run only: if it isn't
// already present system-wide (/usr/share/applications) or for the current
// user (~/.local/share/applications, or $XDG_DATA_HOME/applications), it
// creates one. Running as root installs system-wide; running as a normal
// user installs the per-user copy. Best-effort: any failure (e.g. no write
// permission) is returned but never fatal to the caller — shfm should keep
// starting normally either way.
func EnsureInstalled() error {
	sysPath := filepath.Join(systemDir, fileName)
	usrDir, err := userDir()
	if err != nil {
		return err
	}
	usrPath := filepath.Join(usrDir, fileName)

	if exists(sysPath) || exists(usrPath) {
		return nil // already installed somewhere relevant: nothing to do
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not determine shfm's own executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	content := fmt.Sprintf(template, exe)

	var target string
	if getUID() == 0 {
		target = sysPath
	} else {
		target = usrPath
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(content), 0o644)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
