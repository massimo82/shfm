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

// Package secret encrypts secrets at rest (currently: saved SMB and SFTP
// passwords) before writing them to the configuration file.
//
// The encryption key is derived (via HKDF-SHA256) from a seed specific to
// this machine and this user, NOT stored in the same file as the encrypted
// configuration: this protects against an accidental disclosure of the
// configuration file (backups, a copy on another device, screen sharing, an
// accidental commit to a repository...), but — to be honest — not against
// an attacker who already has full access to this machine as this same
// user, since at that point they could derive the same key. Stronger
// protection would require a system keychain (Keychain/Secret Service/
// Credential Manager), which would however require external dependencies or
// cgo, explicitly excluded from this project.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/hkdf"
)

// keySeedPath returns the path of the file holding the random seed
// specific to this installation, generated only once.
func keySeedPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	dir = filepath.Join(dir, "shfm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, ".keyseed"), nil
}

// loadOrCreateSeed reads the persistent seed, generating it (32 random
// bytes, with 0600 permissions) if it doesn't exist yet.
func loadOrCreateSeed() ([]byte, error) {
	p, err := keySeedPath()
	if err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(p); err == nil && len(data) == 32 {
		return data, nil
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, seed, 0o600); err != nil {
		return nil, err
	}
	return seed, nil
}

// machineTag gathers some additional machine-specific information
// (if available) to include in the key derivation, so that copying just
// the .keyseed file to another machine without the encrypted
// configuration file too still isn't enough on its own.
func machineTag() []byte {
	if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		return data
	}
	if data, err := os.ReadFile("/var/lib/dbus/machine-id"); err == nil {
		return data
	}
	host, _ := os.Hostname()
	return []byte(host)
}

func deriveKey() ([]byte, error) {
	seed, err := loadOrCreateSeed()
	if err != nil {
		return nil, err
	}
	info := append([]byte("shfm-secret-v1|"), machineTag()...)
	r := hkdf.New(sha256.New, seed, nil, info)
	key := make([]byte, 32) // AES-256
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, err
	}
	return key, nil
}

// Encrypt encrypts plaintext with AES-256-GCM and returns the result
// (nonce+ciphertext) base64-encoded, ready to be written into a text
// field of the JSON configuration file.
func Encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	key, err := deriveKey()
	if err != nil {
		return "", fmt.Errorf("deriving the encryption key failed: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt reverses Encrypt. Returns an empty string, with no error, if
// encoded is empty (no secret saved).
func Decrypt(encoded string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	key, err := deriveKey()
	if err != nil {
		return "", fmt.Errorf("deriving the encryption key failed: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("password salvata corrotta: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("password salvata corrotta (troppo corta)")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("could not decrypt the saved password (has the machine key changed?): %w", err)
	}
	return string(plaintext), nil
}
