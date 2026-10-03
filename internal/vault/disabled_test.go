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

package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"shfm/internal/vfs"
)

func TestDisabled(t *testing.T) {
	if Available {
		t.Fatal("Available in a build without the vault tag")
	}
	be := vfs.NewLocalFS("test", "/")
	dir := t.TempDir()
	if _, err := Create(be, dir, "pw", Options{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Unlock(be, dir, "pw"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Unlock: %v", err)
	}
	// A vault is still recognized, to tell the user what the folder is.
	os.WriteFile(filepath.Join(dir, configFile), []byte("{}"), 0o644)
	if !IsVault(be, dir) {
		t.Fatal("IsVault doesn't recognize a vault without the module")
	}
}
