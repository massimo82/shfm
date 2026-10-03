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

//go:build !vault

package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Without the vault module: no "New encrypted vault", and a vault folder
// is entered as a plain one, saying why.
func TestVaultUnavailable(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Box"), 0o755)
	os.WriteFile(filepath.Join(dir, "Box", "vault.json"), []byte("{}"), 0o644)
	m := archiveTestModel(t, dir)

	m.openNewItemChoice()
	if len(m.dialog.Items) != 2 {
		t.Fatalf("New… offers %v", m.dialog.Items)
	}
	m.dialog = Dialog{}

	p := m.activePane()
	moveCursorTo(t, p, "Box")
	m.enterOrOpen()
	if p.Path != filepath.Join(dir, "Box") || !strings.Contains(m.status, "vault") {
		t.Fatalf("path %q, status %q", p.Path, m.status)
	}
}

func TestAboutWithoutVaults(t *testing.T) {
	if strings.Contains(aboutDescription(), "vault") {
		t.Fatalf("About mentions vaults in a build without them: %s", aboutDescription())
	}
}
