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

package vfs

import (
	"io"
	"os"
)

// flushEvery is how much flushFile lets pile up unflushed.
const flushEvery = 16 << 20

// flushFile is a file being written on a removable drive (see
// onRemovable): it's flushed every flushEvery bytes, so a copy's progress
// follows what has really reached the drive, and on Close, so a file
// counts as written only once it's on the drive. *os.File's ReadFrom
// stays promoted, keeping copy_file_range(2) for a copy between files.
type flushFile struct {
	*os.File
	pending int64
}

func (f *flushFile) Write(p []byte) (int, error) {
	n, err := f.File.Write(p)
	if err != nil {
		return n, err
	}
	return n, f.wrote(int64(n))
}

func (f *flushFile) ReadFrom(r io.Reader) (int64, error) {
	n, err := f.File.ReadFrom(r)
	if err != nil {
		return n, err
	}
	return n, f.wrote(n)
}

func (f *flushFile) wrote(n int64) error {
	f.pending += n
	if f.pending < flushEvery {
		return nil
	}
	f.pending = 0
	return f.File.Sync()
}

func (f *flushFile) Close() error {
	if err := f.File.Sync(); err != nil {
		f.File.Close()
		return err
	}
	return f.File.Close()
}
