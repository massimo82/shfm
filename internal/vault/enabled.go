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

//go:build vault

package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"filippo.io/age"

	"shfm/internal/vfs"
)

func init() { Available = true }

// formatVersion is the vault format this shfm writes and reads; a vault
// with a higher one is refused (ErrNewerFormat).
const formatVersion = 1

// scryptWorkFactor is the scrypt cost protecting identity.age, as log2 of
// N: age's default, about a second on a modern machine. A variable so
// that tests can make it cheap.
var scryptWorkFactor = 18

// config is vault.json, in clear: nothing in it is secret (the recipient
// is the identity's public key).
type config struct {
	Format    string    `json:"format"`
	Version   int       `json:"version"`
	Names     NameMode  `json:"names"`
	Recipient string    `json:"recipient"`
	Created   time.Time `json:"created"`
}

const formatName = "shfm-vault"

// state is an unlocked vault, shared by its FS and every FS redialed from
// it, so that they all see the same cached indexes.
type state struct {
	dir string // the vault's folder on the backend
	cfg config

	// mu guards the identity (cleared by Lock) and the index cache, and
	// serializes every change to an index.
	mu       sync.Mutex
	identity age.Identity
	rcpt     age.Recipient
	idx      map[string]*index // by folder path on the backend
	locked   atomic.Bool
	label    string
	identStr string

	// overhead is the size of an age file's header and nonce with this
	// vault's recipient: with it, a file's plaintext size follows from its
	// encrypted size (see plainSize).
	overhead int64
}

// keys returns the identity and recipient, ErrLocked once locked.
func (s *state) keys() (age.Identity, age.Recipient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identity == nil {
		return nil, nil, ErrLocked
	}
	return s.identity, s.rcpt, nil
}

// Vault is an unlocked vault.
type Vault struct {
	fs vfs.FileSystem
	st *state
}

// Create creates a new vault in dir, an existing empty folder on fs,
// protected by password, and returns its recovery key: the age identity
// itself, which opens the vault without the password (UnlockWithKey) and
// decrypts its files with the age tools. The user must keep it safe —
// without both the password and the key, the files are lost.
func Create(fs vfs.FileSystem, dir, password string, opts Options) (string, error) {
	if password == "" {
		return "", errors.New("the password can't be empty")
	}
	if opts.Names == "" {
		opts.Names = NamesEncrypted
	}
	if opts.Names != NamesEncrypted && opts.Names != NamesPlain {
		return "", fmt.Errorf("unknown name mode %q", opts.Names)
	}
	entries, err := fs.List(dir)
	if err != nil {
		return "", err
	}
	if len(entries) > 0 {
		return "", ErrDirNotEmpty
	}

	var identStr, rcptStr string
	if opts.PostQuantum {
		id, err := age.GenerateHybridIdentity()
		if err != nil {
			return "", err
		}
		identStr, rcptStr = id.String(), id.Recipient().String()
	} else {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			return "", err
		}
		identStr, rcptStr = id.String(), id.Recipient().String()
	}
	cfg := config{Format: formatName, Version: formatVersion, Names: opts.Names, Recipient: rcptStr, Created: time.Now().UTC().Truncate(time.Second)}
	st, err := newState(fs, dir, cfg, identStr)
	if err != nil {
		return "", err
	}

	// vault.json last: until it exists, the folder isn't a vault.
	sealed, err := sealIdentity(identStr, rcptStr, cfg.Created, password)
	if err != nil {
		return "", err
	}
	if err := writeSmall(fs, fs.Join(dir, identityFile), sealed); err != nil {
		return "", err
	}
	if err := writeSmall(fs, fs.Join(dir, recoveryFile), []byte(recoveryText(cfg))); err != nil {
		return "", err
	}
	if cfg.Names == NamesEncrypted {
		data, err := st.sealIndex(&index{})
		if err != nil {
			return "", err
		}
		if err := writeSmall(fs, fs.Join(dir, indexFile), data); err != nil {
			return "", err
		}
	}
	if d, ok := fs.(*DispersedFS); ok {
		// The vault's RECOVERY.txt is split like every other file: each
		// part gets, in clear, how to put the vault back together.
		for i, p := range d.parts {
			if err := writeSmall(p.FS, p.FS.Join(p.Dir, recoveryFile), []byte(splitRecoveryText(i))); err != nil {
				return "", err
			}
		}
	}
	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeSmall(fs, fs.Join(dir, configFile), append(cfgData, '\n')); err != nil {
		return "", err
	}
	return identStr, nil
}

// sealIdentity is identity.age: the identity in the format of age-keygen's
// key files, encrypted with password — a file the age command line tool
// accepts as is with -i, asking for the password.
func sealIdentity(identStr, rcptStr string, created time.Time, password string) ([]byte, error) {
	r, err := age.NewScryptRecipient(password)
	if err != nil {
		return nil, err
	}
	r.SetWorkFactor(scryptWorkFactor)
	text := fmt.Sprintf("# shfm vault identity\n# created: %s\n# public key: %s\n%s\n", created.Format(time.RFC3339), rcptStr, identStr)
	return encryptBytes([]byte(text), r)
}

func encryptBytes(data []byte, r age.Recipient) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decryptBytes(data []byte, id age.Identity) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(data), id)
	if err != nil {
		return nil, err
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDamaged, err)
	}
	return out, nil
}

func readConfig(fs vfs.FileSystem, dir string) (config, error) {
	var cfg config
	data, err := readSmall(fs, fs.Join(dir, configFile))
	if err != nil {
		if !exists(fs, fs.Join(dir, configFile)) {
			return cfg, ErrNotVault
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil || cfg.Format != formatName {
		return cfg, fmt.Errorf("%s: %w", configFile, ErrNotVault)
	}
	if cfg.Version > formatVersion {
		return cfg, ErrNewerFormat
	}
	if cfg.Names != NamesEncrypted && cfg.Names != NamesPlain {
		return cfg, fmt.Errorf("%s: unknown name mode %q", configFile, cfg.Names)
	}
	return cfg, nil
}

// Unlock opens the vault in dir, on fs, with its password: ErrWrongPassword
// if it isn't the vault's.
func Unlock(fs vfs.FileSystem, dir, password string) (*Vault, error) {
	cfg, err := readConfig(fs, dir)
	if err != nil {
		return nil, err
	}
	sid, err := age.NewScryptIdentity(password)
	if err != nil {
		return nil, ErrWrongPassword
	}
	var identStr string
	wrong := false
	err = readSmallVersions(fs, fs.Join(dir, identityFile), false, func(data []byte) error {
		text, err := decryptBytes(data, sid)
		var nm *age.NoIdentityMatchError
		if errors.As(err, &nm) {
			wrong = true
			return ErrWrongPassword
		}
		if err != nil {
			return fmt.Errorf("%s: %w: %v", identityFile, ErrDamaged, err)
		}
		identStr, err = parseIdentityText(string(text))
		return err
	})
	if err != nil {
		if wrong {
			return nil, ErrWrongPassword
		}
		return nil, err
	}
	return open(fs, dir, cfg, identStr, ErrDamaged)
}

// UnlockWithKey opens the vault in dir, on fs, with its recovery key — the
// AGE-SECRET-KEY-… line Create returned, or a whole age-keygen key file:
// ErrWrongKey if it isn't the vault's.
func UnlockWithKey(fs vfs.FileSystem, dir, recoveryKey string) (*Vault, error) {
	cfg, err := readConfig(fs, dir)
	if err != nil {
		return nil, err
	}
	identStr, err := parseIdentityText(recoveryKey)
	if err != nil {
		return nil, ErrWrongKey
	}
	return open(fs, dir, cfg, identStr, ErrWrongKey)
}

// parseIdentityText returns the one identity in an age key file's text.
func parseIdentityText(text string) (string, error) {
	ids, err := age.ParseIdentities(strings.NewReader(text))
	if err != nil {
		return "", err
	}
	if len(ids) != 1 {
		return "", errors.New("expected exactly one identity")
	}
	switch id := ids[0].(type) {
	case *age.X25519Identity:
		return id.String(), nil
	case *age.HybridIdentity:
		return id.String(), nil
	}
	return "", errors.New("unsupported identity type")
}

// open unlocks the vault with identStr, checking it is the identity of the
// vault's recipient (mismatch: the error returned).
func open(fs vfs.FileSystem, dir string, cfg config, identStr string, mismatch error) (*Vault, error) {
	st, err := newState(fs, dir, cfg, identStr)
	if err != nil {
		return nil, err
	}
	if publicKey(st.identity) != cfg.Recipient {
		return nil, mismatch
	}
	return &Vault{fs: fs, st: st}, nil
}

func publicKey(id age.Identity) string {
	switch id := id.(type) {
	case *age.X25519Identity:
		return id.Recipient().String()
	case *age.HybridIdentity:
		return id.Recipient().String()
	}
	return ""
}

func newState(fs vfs.FileSystem, dir string, cfg config, identStr string) (*state, error) {
	ids, err := age.ParseIdentities(strings.NewReader(identStr))
	if err != nil {
		return nil, err
	}
	rcpts, err := age.ParseRecipients(strings.NewReader(cfg.Recipient))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", configFile, err)
	}
	st := &state{
		dir: dir, cfg: cfg, identity: ids[0], rcpt: rcpts[0], identStr: identStr,
		idx:   map[string]*index{},
		label: fs.Base(dir),
	}
	empty, err := encryptBytes(nil, st.rcpt)
	if err != nil {
		return nil, err
	}
	st.overhead = int64(len(empty)) - tagSize // header + nonce: an empty file is one empty chunk
	return st, nil
}

// FS returns the vault's content as a file system, its root "/" being the
// vault's folder. Every FS of a vault stops working once it is locked.
// Closing it doesn't close the backend, which stays its opener's.
func (v *Vault) FS() vfs.FileSystem { return newFS(v.st, v.fs, false) }

// On returns the same unlocked vault reached through fs, another
// connection to the backend it was unlocked on (another pane's).
func (v *Vault) On(fs vfs.FileSystem) *Vault { return &Vault{fs: fs, st: v.st} }

// Dir returns the vault's folder on its backend.
func (v *Vault) Dir() string { return v.st.dir }

// Locked reports whether Lock was called.
func (v *Vault) Locked() bool { return v.st.locked.Load() }

// ChangePassword sets a new password: only identity.age is re-encrypted,
// the files are untouched. The old password stops working at once (no
// copy of the old identity.age is kept).
func (v *Vault) ChangePassword(password string) error {
	if password == "" {
		return errors.New("the password can't be empty")
	}
	v.st.mu.Lock()
	identStr := v.st.identStr
	v.st.mu.Unlock()
	if identStr == "" {
		return ErrLocked
	}
	sealed, err := sealIdentity(identStr, v.st.cfg.Recipient, time.Now().UTC().Truncate(time.Second), password)
	if err != nil {
		return err
	}
	return replace(v.fs, v.fs.Join(v.st.dir, identityFile), sealed, false)
}

// RecoveryKey returns the vault's identity (AGE-SECRET-KEY-…), to export it
// for the age tools or show it again as the recovery key.
func (v *Vault) RecoveryKey() (string, error) {
	v.st.mu.Lock()
	defer v.st.mu.Unlock()
	if v.st.identStr == "" {
		return "", ErrLocked
	}
	return v.st.identStr, nil
}

// Options returns the choices the vault was created with.
func (v *Vault) Options() Options {
	return Options{Names: v.st.cfg.Names, PostQuantum: strings.HasPrefix(v.st.cfg.Recipient, "age1pq1")}
}

// Lock locks the vault: its FSs fail with ErrLocked from now on and the
// keys and cached indexes are dropped. Go can't guarantee the key material
// is wiped from memory (the garbage collector may have copied it), only
// that shfm no longer holds it.
func (v *Vault) Lock() {
	v.st.mu.Lock()
	defer v.st.mu.Unlock()
	v.st.locked.Store(true)
	v.st.identity, v.st.identStr = nil, ""
	v.st.idx = map[string]*index{}
}

// Sizes of the age payload format (STREAM): every chunk of up to 64 KiB of
// plaintext carries a 16-byte tag, and an empty file is one empty chunk.
const (
	chunkSize = 64 << 10
	tagSize   = 16
)

// cipherSize is the size of an age file holding n bytes of plaintext, for
// a header + nonce of overhead bytes.
func cipherSize(overhead, n int64) int64 {
	chunks := (n + chunkSize - 1) / chunkSize
	if chunks == 0 {
		chunks = 1
	}
	return overhead + n + chunks*tagSize
}

// plainSize is the plaintext size of an age file of c bytes (false when no
// plaintext size gives c: a file with another header, or damaged).
func plainSize(overhead, c int64) (int64, bool) {
	payload := c - overhead
	if payload < tagSize {
		return 0, false
	}
	chunks := (payload + chunkSize + tagSize - 1) / (chunkSize + tagSize)
	n := payload - chunks*tagSize
	if n < 0 || cipherSize(overhead, n) != c {
		return 0, false
	}
	return n, true
}

// errNotExist builds a not-found error for path p.
func errNotExist(op, p string) error {
	return &os.PathError{Op: op, Path: p, Err: os.ErrNotExist}
}
