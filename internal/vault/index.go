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
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"shfm/internal/vfs"
)

// index is a folder's .index.age, in a vault with encrypted names: the
// folder's entries by name. A file entry's content is the age file
// "<ID>.age" next to the index, a folder entry's the folder "<ID>".
type index struct {
	Version int     `json:"version"`
	Entries []entry `json:"entries"`

	byName map[string]int // position in Entries
}

type entry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Dir   bool   `json:"dir,omitempty"`
	Size  int64  `json:"size"`  // plaintext bytes; 0 for a folder
	MTime int64  `json:"mtime"` // Unix nanoseconds
}

// blob is the entry's name on the backend.
func (e entry) blob() string {
	if e.Dir {
		return e.ID
	}
	return e.ID + ".age"
}

func (x *index) reindex() {
	x.byName = make(map[string]int, len(x.Entries))
	for i, e := range x.Entries {
		x.byName[e.Name] = i
	}
}

func (x *index) get(name string) (entry, bool) {
	i, ok := x.byName[name]
	if !ok {
		return entry{}, false
	}
	return x.Entries[i], true
}

func (x *index) put(e entry) {
	if i, ok := x.byName[e.Name]; ok {
		x.Entries[i] = e
		return
	}
	x.byName[e.Name] = len(x.Entries)
	x.Entries = append(x.Entries, e)
}

func (x *index) remove(name string) {
	i, ok := x.byName[name]
	if !ok {
		return
	}
	x.Entries = append(x.Entries[:i], x.Entries[i+1:]...)
	x.reindex()
}

// clone is a copy to change, so that a failed save leaves the cached
// index as it was.
func (x *index) clone() *index {
	c := &index{Version: x.Version, Entries: append([]entry(nil), x.Entries...)}
	c.reindex()
	return c
}

// newID returns a random name for an entry on the backend: 128 bits,
// lowercase base32 (26 characters, safe on every backend).
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return lowerBase32.EncodeToString(b)
}

var lowerBase32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func (s *state) sealIndex(x *index) ([]byte, error) {
	out := index{Version: 1, Entries: append([]entry(nil), x.Entries...)}
	if out.Entries == nil {
		out.Entries = []entry{}
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Name < out.Entries[j].Name })
	data, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return encryptBytes(data, s.rcpt)
}

// loadIndex returns the index of dir (a folder on the backend), from the
// cache unless fresh. s.mu must be held.
func (s *state) loadIndex(fs vfs.FileSystem, dir string, fresh bool) (*index, error) {
	if s.identity == nil {
		return nil, ErrLocked
	}
	if x, ok := s.idx[dir]; ok && !fresh {
		return x, nil
	}
	var x *index
	err := readSmallVersions(fs, fs.Join(dir, indexFile), true, func(data []byte) error {
		plain, err := decryptBytes(data, s.identity)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrDamaged, err)
		}
		var got index
		if err := json.Unmarshal(plain, &got); err != nil {
			return fmt.Errorf("%w: %v", ErrDamaged, err)
		}
		got.reindex()
		x = &got
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrDamaged) {
			return nil, fmt.Errorf("folder index %s: %w", fs.Join(dir, indexFile), err)
		}
		return nil, err
	}
	s.idx[dir] = x
	return x, nil
}

// saveIndex writes x as the index of dir and caches it. s.mu must be held.
func (s *state) saveIndex(fs vfs.FileSystem, dir string, x *index) error {
	if s.identity == nil {
		return ErrLocked
	}
	data, err := s.sealIndex(x)
	if err != nil {
		return err
	}
	if err := replace(fs, fs.Join(dir, indexFile), data, true); err != nil {
		delete(s.idx, dir)
		return err
	}
	s.idx[dir] = x
	return nil
}
