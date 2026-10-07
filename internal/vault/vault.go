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

// Package vault implements encrypted vaults: a folder, on any backend,
// that only holds encrypted files, shown in clear by the vault's own
// vfs.FileSystem once unlocked with its password.
//
// Every file in a vault is a standard age file (https://age-encryption.org,
// library filippo.io/age), so a vault can always be recovered without shfm,
// with the age or rage command line tools (see RECOVERY.txt, written in
// every vault). The keys are layered:
//
//	password ──scrypt──► identity.age ──► age identity (X25519, or hybrid
//	                                       ML-KEM-768 + X25519)
//	                                           │
//	                              every file is encrypted to it
//
// so the costly scrypt runs once per unlock, not once per file, and
// changing the password only re-encrypts identity.age.
//
// File names are encrypted too, by default: on the backend each file and
// folder is named by a random ID, and each folder holds a .index.age (an
// age-encrypted JSON) mapping its IDs to the real names, sizes and
// modification times. With NamesPlain, files keep their names (plus
// ".age") and folders are plain folders.
//
// The module is optional, added with the "vault" build tag: without it,
// Available is false and every operation fails with ErrUnavailable.
package vault

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"shfm/internal/vfs"
)

// Available reports whether this build includes the vault module (build
// tag "vault").
var Available bool

var (
	// ErrUnavailable is returned by every operation in a build without the
	// vault module.
	ErrUnavailable = errors.New("vaults aren't in this build (build tag \"vault\")")
	// ErrNotVault: the folder holds no vault (no vault.json).
	ErrNotVault = errors.New("not an encrypted vault")
	// ErrDirNotEmpty: a new vault can only be created in an empty folder.
	ErrDirNotEmpty = errors.New("a vault can only be created in an empty folder")
	// ErrWrongPassword: the password doesn't open the vault.
	ErrWrongPassword = errors.New("wrong password")
	// ErrWrongKey: the recovery key isn't this vault's.
	ErrWrongKey = errors.New("the recovery key does not belong to this vault")
	// ErrLocked: the vault has been locked, its files can't be reached
	// until it is unlocked again.
	ErrLocked = errors.New("the vault is locked")
	// ErrDamaged: a vault file failed decryption or its integrity check —
	// damaged, truncated or tampered with.
	ErrDamaged = errors.New("encrypted data is damaged or has been tampered with")
	// ErrReservedName: the name is used by the vault's own files.
	ErrReservedName = errors.New("this name is reserved by the vault")
	// ErrNameTooLong: the name, as stored on the backend, would be too
	// long (vaults with plain names only).
	ErrNameTooLong = errors.New("name too long")
	// ErrVaultInVault: a vault can't be stored inside another — refused
	// when a folder of a vault would get both a vault's vault.json and its
	// identity.age, however they're written (copied by shfm, extracted from
	// an archive, saved by an application through FUSE).
	ErrVaultInVault = errors.New("an encrypted vault can't be stored inside another vault")
	// ErrNewerFormat: the vault was made by a newer version of shfm.
	ErrNewerFormat = errors.New("the vault was created by a newer version of shfm")
)

// NameMode chooses how a vault stores file and folder names.
type NameMode string

const (
	// NamesEncrypted stores random IDs on the backend, the names being
	// kept in each folder's encrypted index (the default).
	NamesEncrypted NameMode = "encrypted"
	// NamesPlain keeps the names visible: "name.ext.age" files in plain
	// folders. More interoperable, but anyone with access to the backend
	// reads the names and the folder structure.
	NamesPlain NameMode = "plain"
)

// Options are the choices made when creating a vault.
type Options struct {
	// Names: NamesEncrypted when empty.
	Names NameMode
	// PostQuantum uses a hybrid ML-KEM-768 + X25519 identity, safe against
	// future quantum computers: decrypting it needs age 1.3 or later, or
	// another implementation with the age-plugin-pq plugin (see
	// RECOVERY.txt). The default is a classic X25519 identity, which every
	// age implementation reads.
	PostQuantum bool
}

// The vault's own files, in its root folder.
const (
	configFile   = "vault.json"
	identityFile = "identity.age"
	recoveryFile = "RECOVERY.txt"
	indexFile    = ".index.age"
)

// IsVault reports whether dir, on fs, holds a vault — in any build, so that
// a build without the module can still tell the user what the folder is.
func IsVault(fs vfs.FileSystem, dir string) bool {
	e, err := fs.Stat(fs.Join(dir, configFile))
	return err == nil && !e.IsDir
}

// SpoolDir returns the folder for decrypted working copies:
// $XDG_RUNTIME_DIR/shfm, a per-user tmpfs (memory, never a disk), created
// private to the user; false without $XDG_RUNTIME_DIR. Shared with the UI,
// which opens vault files with external applications the same way when it
// can't through the FUSE mount. A variable so that tests can point it
// elsewhere.
var SpoolDir = func() (string, bool) {
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		return "", false
	}
	dir := filepath.Join(rt, "shfm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false
	}
	return dir, true
}

// Part is one of a split vault's three locations (see DispersedFS): a
// folder on a source. FS is nil when the source couldn't be reached.
type Part struct {
	FS  vfs.FileSystem
	Dir string
}

// ErrPartUnavailable: a change needs all three parts of a split vault.
var ErrPartUnavailable = errors.New("a vault part is unreachable: read-only until all are back")

// RepairStats says what RepairDispersed did.
type RepairStats struct {
	Folders int // folders created on a part missing them
	Shards  int // shards rebuilt
	Removed int // stale shards and folders removed
	Lost    int // files with fewer than two shards: unrecoverable
}

// splitRecoveryTitle starts a split vault part's RECOVERY.txt (see
// splitRecoveryText).
const splitRecoveryTitle = "SPLIT ENCRYPTED VAULT"

// SplitPart reports whether dir on fs is a part of a split vault, and
// which (0-2): its RECOVERY.txt, in clear, says so.
func SplitPart(fs vfs.FileSystem, dir string) (int, bool) {
	r, err := fs.Open(fs.Join(dir, recoveryFile))
	if err != nil {
		return 0, false
	}
	defer r.Close()
	head, _ := io.ReadAll(io.LimitReader(r, 512))
	text := string(head)
	if !strings.HasPrefix(text, splitRecoveryTitle) {
		return 0, false
	}
	i := strings.Index(text, "This folder is part ")
	if i < 0 {
		return 0, false
	}
	var part int
	if _, err := fmt.Sscanf(text[i:], "This folder is part %d of 3", &part); err != nil || part < 1 || part > 3 {
		return 0, false
	}
	return part - 1, true
}
