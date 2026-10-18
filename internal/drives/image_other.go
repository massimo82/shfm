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

//go:build !linux

package drives

import (
	"context"
	"errors"
	"io"
)

// DiskSize is unknown on this platform.
func DiskSize(wholeDiskPath string) int64 { return 0 }

// WriteImage is not supported on this platform.
func WriteImage(ctx context.Context, wholeDiskPath string, src io.Reader, size int64, verify bool, progress func(ImageProgress)) error {
	return errors.New("writing images isn't supported here")
}
