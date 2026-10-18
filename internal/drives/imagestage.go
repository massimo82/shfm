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

package drives

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"syscall"

	"shfm/internal/archive"
)

var (
	// ErrNoImage is the error of an archive holding no disk image.
	ErrNoImage = errors.New("no ISO/IMG image in the archive")
	// ErrManyImages is the error of an archive holding more than one.
	ErrManyImages = errors.New("more than one image in the archive")
	// ErrStageNoSpace is (wrapped in) the error of a folder too full to
	// extract the image into.
	ErrStageNoSpace = errors.New("not enough space")
)

// stageReserve is what extracting an image leaves free in the folder it
// goes to: at least this many bytes, or a tenth of the filesystem if
// more, since /tmp is often a tmpfs, whose space is the machine's memory.
const stageReserve = 512 << 20

// diskUsageOf tells a folder's filesystem size and free space: a
// variable, so tests can simulate a full one.
var diskUsageOf = diskUsage

// StagedImage is a disk image extracted or copied into a temporary file,
// for WriteImage to write it from there.
type StagedImage struct {
	Path string // the temporary file
	Name string // the image's own name
	Size int64
}

// StageImage extracts the disk image (see IsImageName) the archive src
// holds into a new file in dir. onBytes, when not nil, is told the
// bytes extracted so far, of total (-1 when the archive doesn't say).
//
// The whole archive is read: one holding no image is ErrNoImage, one
// holding several ErrManyImages, and in either case nothing is left in
// dir. So is it when dir is too full for the image (ErrStageNoSpace),
// checked before extracting it when its size is known, and while
// extracting it.
func StageImage(ctx context.Context, src archive.Source, dir string, onBytes func(done, total int64)) (StagedImage, error) {
	var img StagedImage
	err := archive.Walk(ctx, src, func(e archive.Entry, r io.Reader) error {
		if e.Type != archive.TypeFile || !IsImageName(path.Base(e.Name)) {
			return nil
		}
		if e.Skip != nil {
			return fmt.Errorf("%s: %w", path.Base(e.Name), e.Skip)
		}
		if img.Path != "" {
			return ErrManyImages
		}
		if err := checkStageSpace(dir, max(e.Size, 0)); err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, "shfm-image-*-"+path.Base(e.Name))
		if err != nil {
			return err
		}
		img.Path, img.Name = f.Name(), path.Base(e.Name)
		img.Size, err = copyStaged(ctx, f, r, dir, e.Size, onBytes)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return err
	})
	if err == nil && img.Path == "" {
		err = ErrNoImage
	}
	if err != nil {
		if img.Path != "" {
			os.Remove(img.Path)
		}
		return StagedImage{}, err
	}
	return img, nil
}

// StageFile copies the image r, named name, of total bytes (-1 when
// unknown), into a new file in dir, as StageImage does for one inside an
// archive: so that an image on a network source is written from a local
// copy, which a dropped connection can't cut halfway.
func StageFile(ctx context.Context, r io.Reader, name, dir string, total int64, onBytes func(done, total int64)) (StagedImage, error) {
	if err := checkStageSpace(dir, max(total, 0)); err != nil {
		return StagedImage{}, err
	}
	f, err := os.CreateTemp(dir, "shfm-image-*-"+path.Base(name))
	if err != nil {
		return StagedImage{}, err
	}
	size, err := copyStaged(ctx, f, r, dir, total, onBytes)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return StagedImage{}, err
	}
	return StagedImage{Path: f.Name(), Name: path.Base(name), Size: size}, nil
}

// copyStaged copies r into f, an image being extracted into dir,
// checking dir keeps its reserve every few MiB.
func copyStaged(ctx context.Context, f *os.File, r io.Reader, dir string, total int64, onBytes func(done, total int64)) (int64, error) {
	const checkEvery = 64 << 20
	buf := make([]byte, 1<<20)
	var done, unchecked int64
	for {
		if err := ctx.Err(); err != nil {
			return done, err
		}
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return done, stageWriteError(dir, werr)
			}
			done += int64(n)
			if unchecked += int64(n); unchecked >= checkEvery {
				if err := checkStageSpace(dir, 0); err != nil {
					return done, err
				}
				unchecked = 0
			}
			if onBytes != nil {
				onBytes(done, total)
			}
		}
		if err == io.EOF {
			return done, nil
		}
		if err != nil {
			return done, err
		}
	}
}

// checkStageSpace fails when writing need more bytes into dir would leave
// it less than its reserve (see stageReserve).
func checkStageSpace(dir string, need int64) error {
	total, free := diskUsageOf(dir)
	if total == 0 {
		return nil // unknown here
	}
	reserve := max(uint64(stageReserve), total/10)
	if free < reserve || uint64(need) > free-reserve {
		return fmt.Errorf("%w in %s", ErrStageNoSpace, dir)
	}
	return nil
}

// stageWriteError is err, a write into dir that failed, said in short
// when it's for lack of space.
func stageWriteError(dir string, err error) error {
	if errors.Is(err, syscall.ENOSPC) {
		return fmt.Errorf("%w in %s", ErrStageNoSpace, dir)
	}
	return err
}
