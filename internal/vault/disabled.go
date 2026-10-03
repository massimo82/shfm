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

import "shfm/internal/vfs"

func init() { Available = false }

// Vault is an unlocked vault.
type Vault struct{}

// Create creates a new vault in dir.
func Create(fs vfs.FileSystem, dir, password string, opts Options) (string, error) {
	return "", ErrUnavailable
}

// Unlock opens the vault in dir with its password.
func Unlock(fs vfs.FileSystem, dir, password string) (*Vault, error) { return nil, ErrUnavailable }

// UnlockWithKey opens the vault in dir with its recovery key.
func UnlockWithKey(fs vfs.FileSystem, dir, recoveryKey string) (*Vault, error) {
	return nil, ErrUnavailable
}

// FS returns the vault's content as a file system.
func (v *Vault) FS() vfs.FileSystem { return nil }

// On returns the vault reached through fs.
func (v *Vault) On(fs vfs.FileSystem) *Vault { return v }

// Dir returns the vault's folder.
func (v *Vault) Dir() string { return "" }

// Locked reports whether the vault is locked.
func (v *Vault) Locked() bool { return true }

// ChangePassword sets a new password.
func (v *Vault) ChangePassword(password string) error { return ErrUnavailable }

// RecoveryKey returns the vault's identity.
func (v *Vault) RecoveryKey() (string, error) { return "", ErrUnavailable }

// Options returns the choices the vault was created with.
func (v *Vault) Options() Options { return Options{} }

// Lock locks the vault.
func (v *Vault) Lock() {}

// Split returns split vault storage over parts.
func Split(label string, parts [3]Part) vfs.FileSystem { return nil }

// IsSplit reports whether fs is split vault storage.
func IsSplit(fs vfs.FileSystem) bool { return false }

// UnavailableParts lists the parts of fs that aren't connected.
func UnavailableParts(fs vfs.FileSystem) []int { return nil }

// RepairDispersed makes the parts of fs whole again.
func RepairDispersed(fs vfs.FileSystem) (RepairStats, error) { return RepairStats{}, ErrUnavailable }
