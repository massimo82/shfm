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

package mirror

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// fileState records a file as it was right after the generic engine last
// copied it (or found it already up to date). The VFS can't set a file's
// modification time, so a copied file's mtime is the copy time: comparing
// source and destination mtimes directly would be meaningless. Instead,
// a file is up to date when both sides still look exactly as recorded.
type fileState struct {
	SrcSize int64     `json:"src_size"`
	SrcMod  time.Time `json:"src_mod"`
	DstSize int64     `json:"dst_size"`
	DstMod  time.Time `json:"dst_mod"`
}

// manifest maps each mirrored file's path, relative to the mirror root
// ("" for a single-file mirror), to its fileState.
type manifest map[string]fileState

func loadManifest(path string) manifest {
	m := manifest{}
	if path == "" {
		return m
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	if json.Unmarshal(data, &m) != nil {
		return manifest{}
	}
	return m
}

// save writes the manifest atomically (temp file + rename), so a crash
// mid-write never leaves a truncated one behind.
func (m manifest) save(path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// StateDir is where per-mirror state files live:
// $XDG_STATE_HOME/shfm/mirror (default ~/.local/state/shfm/mirror).
func StateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "shfm", "mirror")
}
