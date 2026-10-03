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
	"errors"
	"fmt"
	"io"
	"os"

	"shfm/internal/vfs"
)

// maxSmallFile bounds the vault's own files read whole into memory (the
// configuration, the identity, a folder's index): far above what a folder
// of a hundred thousand entries needs.
const maxSmallFile = 64 << 20

// The suffixes of a small file's previous and next versions, see replace.
const (
	tmpSuffix = ".tmp"
	bakSuffix = ".bak"
)

func exists(fs vfs.FileSystem, p string) bool {
	_, err := fs.Stat(p)
	return err == nil
}

func readSmall(fs vfs.FileSystem, p string) ([]byte, error) {
	r, err := fs.Open(p)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, maxSmallFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSmallFile {
		return nil, fmt.Errorf("%s: file too large", p)
	}
	return data, nil
}

// writeSmall writes data as the whole content of p, declaring its size up
// front where the backend needs it (MTP).
func writeSmall(fs vfs.FileSystem, p string, data []byte) error {
	var w io.WriteCloser
	var err error
	if sc, ok := fs.(vfs.SizedCreator); ok {
		w, err = sc.CreateSized(p, int64(len(data)))
	} else {
		w, err = fs.Create(p)
	}
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// removeIfExists removes p, if there is anything to remove.
func removeIfExists(fs vfs.FileSystem, p string) error {
	if !exists(fs, p) {
		return nil
	}
	return fs.Remove(p)
}

// replace writes data as the new content of p so that an interruption at
// any point leaves a complete version to read back (see readSmallVersions):
// the new content goes to p.tmp first; then, with keepBak, the current p
// becomes p.bak (otherwise it is removed); last, p.tmp becomes p. Backends
// without Rename get a copy instead.
func replace(fs vfs.FileSystem, p string, data []byte, keepBak bool) error {
	tmp := p + tmpSuffix
	if err := writeSmall(fs, tmp, data); err != nil {
		removeIfExists(fs, tmp)
		return err
	}
	if exists(fs, p) {
		if keepBak {
			bak := p + bakSuffix
			if err := removeIfExists(fs, bak); err != nil {
				return err
			}
			if err := renameOrCopy(fs, p, bak); err != nil {
				return err
			}
		} else if err := fs.Remove(p); err != nil {
			return err
		}
	}
	return renameOrCopy(fs, tmp, p)
}

// renameOrCopy moves the small file from to to, a path that doesn't exist.
func renameOrCopy(fs vfs.FileSystem, from, to string) error {
	err := fs.Rename(from, to)
	if !errors.Is(err, vfs.ErrNotSupported) {
		return err
	}
	data, err := readSmall(fs, from)
	if err != nil {
		return err
	}
	if err := writeSmall(fs, to, data); err != nil {
		return err
	}
	return fs.Remove(from)
}

// readSmallVersions reads the small file p written by replace, through
// decode: p itself, or else the version an interrupted replace left (p.tmp,
// complete if it decodes, as every vault file is authenticated), or else
// the previous one (p.bak). The error is the first version's that exists,
// os.ErrNotExist (wrapped) if none does.
func readSmallVersions(fs vfs.FileSystem, p string, withBak bool, decode func([]byte) error) error {
	paths := []string{p, p + tmpSuffix}
	if withBak {
		paths = append(paths, p+bakSuffix)
	}
	var first error
	for _, q := range paths {
		data, err := readSmall(fs, q)
		if err == nil {
			if err = decode(data); err == nil {
				return nil
			}
		} else if !exists(fs, q) {
			continue
		}
		if first == nil {
			first = err
		}
	}
	if first == nil {
		return fmt.Errorf("%s: %w", p, os.ErrNotExist)
	}
	return first
}
