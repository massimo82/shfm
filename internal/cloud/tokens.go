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

//go:build cloud

package cloud

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"

	"shfm/internal/secret"
)

// The accounts' OAuth tokens live in a file of their own,
// $XDG_CONFIG_HOME/shfm/cloud-tokens.json, each encrypted at rest (see
// package secret) — not in config.json: a service may hand out a new
// refresh token whenever the access token is refreshed (Microsoft does,
// every time), from whichever goroutine is using the account, while
// config.json belongs to the UI.

var (
	tokenMu sync.Mutex
	// forgotten are the accounts removed during this run: a source still
	// open on one may refresh its token, which mustn't bring it back.
	forgotten = map[string]bool{}
)

// tokensPath is a variable so tests can use a temporary file.
var tokensPath = func() (string, error) {
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
	return filepath.Join(dir, "cloud-tokens.json"), nil
}

// readTokensLocked reads the file: account ID -> encrypted token JSON.
func readTokensLocked() (map[string]string, error) {
	p, err := tokensPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	return m, nil
}

// writeTokensLocked replaces the file atomically, so that a crash or a
// second shfm process never leaves it half written.
func writeTokensLocked(m map[string]string) error {
	p, err := tokensPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".cloud-tokens-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// loadToken returns the saved token of account id.
func loadToken(id string) (*oauth2.Token, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	m, err := readTokensLocked()
	if err != nil {
		return nil, err
	}
	enc, ok := m[id]
	if !ok {
		return nil, ErrAuthorization
	}
	plain, err := secret.Decrypt(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypting the account's token: %w", err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(plain), &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

// saveToken stores tok as account id's token.
func saveToken(id string, tok *oauth2.Token) error {
	data, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	enc, err := secret.Encrypt(string(data))
	if err != nil {
		return err
	}
	tokenMu.Lock()
	defer tokenMu.Unlock()
	if forgotten[id] {
		return nil
	}
	m, err := readTokensLocked()
	if err != nil {
		return err
	}
	m[id] = enc
	return writeTokensLocked(m)
}

// Forget deletes account id's token, when the user removes the account:
// shfm can no longer use it. The authorization itself stays valid with the
// service until revoked there (in the account's security settings), as
// the services offer no common way to revoke it.
func Forget(id string) error {
	tokenMu.Lock()
	defer tokenMu.Unlock()
	forgotten[id] = true
	m, err := readTokensLocked()
	if err != nil {
		return err
	}
	if _, ok := m[id]; !ok {
		return nil
	}
	delete(m, id)
	return writeTokensLocked(m)
}

// newAccountID returns a random ID for a newly authorized account.
func newAccountID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// savingTokenSource hands out the account's tokens, refreshing them when
// they expire, and saves every new one, so the next run starts from the
// latest refresh token. A refresh the service refuses (the authorization
// was revoked, or expired) is reported as ErrAuthorization.
type savingTokenSource struct {
	id   string
	base oauth2.TokenSource // an oauth2.ReuseTokenSource

	mu   sync.Mutex
	last *oauth2.Token
}

func newSavingTokenSource(cfg *oauth2.Config, id string, tok *oauth2.Token) *savingTokenSource {
	return &savingTokenSource{
		id:   id,
		base: oauth2.ReuseTokenSource(tok, cfg.TokenSource(bgContext(), tok)),
		last: tok,
	}
}

func (s *savingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := s.base.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || re.Response != nil && re.Response.StatusCode == 401) {
			return nil, fmt.Errorf("%w (%v)", ErrAuthorization, err)
		}
		return nil, err
	}
	s.mu.Lock()
	changed := s.last == nil || tok.AccessToken != s.last.AccessToken || tok.RefreshToken != s.last.RefreshToken
	s.last = tok
	s.mu.Unlock()
	if changed {
		if err := saveToken(s.id, tok); err != nil {
			return nil, fmt.Errorf("saving the refreshed token: %w", err)
		}
	}
	return tok, nil
}
