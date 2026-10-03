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

import "shfm/internal/vfs"

// Some optional interfaces are only right for a vault when the backend has
// them, since callers decide on their mere presence:
//
//   - vfs.Redialer: a new connection to the backend (the FUSE mount
//     reconnects through it) — or, over the local backend, which has no
//     connection to open, the same backend: the FUSE mount needs a
//     Redialer or a SingleSession to mount a source at all;
//   - vfs.SingleSession: the backend accepts one connection only, so the
//     FUSE mount must share it;
//   - vfs.DirSizer: folder sizes are computed eagerly, which the UI only
//     wants where listing is cheap (the local backend).
//
// withCaps wraps the FS in the type implementing exactly the backend's.
func withCaps(f *FS) vfs.FileSystem {
	_, r := f.inner.(vfs.Redialer)
	if _, local := f.inner.(vfs.LocalPath); local {
		r = true
	}
	_, s := f.inner.(vfs.SingleSession)
	_, d := f.inner.(vfs.DirSizer)
	switch {
	case r && s && d:
		return fsRSD{f}
	case r && s:
		return fsRS{f}
	case r && d:
		return fsRD{f}
	case s && d:
		return fsSD{f}
	case r:
		return fsR{f}
	case s:
		return fsS{f}
	case d:
		return fsD{f}
	}
	return f
}

type (
	fsR   struct{ *FS }
	fsS   struct{ *FS }
	fsD   struct{ *FS }
	fsRS  struct{ *FS }
	fsRD  struct{ *FS }
	fsSD  struct{ *FS }
	fsRSD struct{ *FS }
)

func (f fsR) Redial() (vfs.FileSystem, error)   { return f.redial() }
func (f fsRS) Redial() (vfs.FileSystem, error)  { return f.redial() }
func (f fsRD) Redial() (vfs.FileSystem, error)  { return f.redial() }
func (f fsRSD) Redial() (vfs.FileSystem, error) { return f.redial() }

func (fsS) SingleSession()   {}
func (fsRS) SingleSession()  {}
func (fsSD) SingleSession()  {}
func (fsRSD) SingleSession() {}

func (f fsD) DirSize(p string) (int64, int64, error)   { return f.dirSize(p) }
func (f fsRD) DirSize(p string) (int64, int64, error)  { return f.dirSize(p) }
func (f fsSD) DirSize(p string) (int64, int64, error)  { return f.dirSize(p) }
func (f fsRSD) DirSize(p string) (int64, int64, error) { return f.dirSize(p) }

var (
	_ vfs.Redialer      = fsR{}
	_ vfs.SingleSession = fsS{}
	_ vfs.DirSizer      = fsD{}
	_ vfs.Redialer      = fsRS{}
	_ vfs.SingleSession = fsRS{}
	_ vfs.Redialer      = fsRD{}
	_ vfs.DirSizer      = fsRD{}
	_ vfs.SingleSession = fsSD{}
	_ vfs.DirSizer      = fsSD{}
	_ vfs.Redialer      = fsRSD{}
	_ vfs.SingleSession = fsRSD{}
	_ vfs.DirSizer      = fsRSD{}
)
