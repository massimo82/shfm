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
// Package origperm remembers the permissions an entry had before shfm first
// changed them — mode, and owner/group when the backend reports them — so
// they can be put back later, after an emergency fix, even across restarts.
// Only the first change of a path is recorded: further edits keep the
// original, and restoring forgets it.
package origperm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Original is what an entry had before its first change.
type Original struct {
	Mode  os.FileMode `json:"mode"`
	Owner string      `json:"owner,omitempty"` // empty: ownership not known, not restored
	Group string      `json:"group,omitempty"`
	Saved time.Time   `json:"saved"`
}

// Store is the set of recorded originals, keyed by source and path (see
// the UI's key), and the file that keeps them. Safe for concurrent use:
// changes are recorded from the goroutine that applied them.
type Store struct {
	mu   sync.Mutex
	path string
	m    map[string]Original
}

// DefaultPath is $XDG_STATE_HOME/shfm/original-permissions.json (default
// ~/.local/state/shfm/...), or "" when no home can be found.
func DefaultPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "shfm", "original-permissions.json")
}

// Load reads the store kept at path; a missing or unreadable file is an
// empty store. An empty path keeps the store in memory only.
func Load(path string) *Store {
	s := &Store{path: path, m: map[string]Original{}}
	if path == "" {
		return s
	}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &s.m)
		if s.m == nil {
			s.m = map[string]Original{}
		}
	}
	return s
}

// Get returns the original recorded for key.
func (s *Store) Get(key string) (Original, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.m[key]
	return o, ok
}

// Remember records o for key, unless an original is already recorded:
// that one, older, is the true original.
func (s *Store) Remember(key string, o Original) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[key]; ok {
		return nil
	}
	if o.Saved.IsZero() {
		o.Saved = time.Now()
	}
	s.m[key] = o
	return s.save()
}

// Forget drops key's original, once restored.
func (s *Store) Forget(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[key]; !ok {
		return nil
	}
	delete(s.m, key)
	return s.save()
}

// save writes the store atomically (temp file + rename), so a crash
// mid-write never leaves a truncated one behind. Callers hold mu.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
