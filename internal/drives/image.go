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
	"errors"
	"io"
	"strings"
)

// IsImageName reports whether name is a disk image shfm writes onto a
// drive as it is, byte by byte: an ISO or a raw ".img".
func IsImageName(name string) bool {
	lower := strings.ToLower(name)
	return (strings.HasSuffix(lower, ".iso") || strings.HasSuffix(lower, ".img")) &&
		len(name) > len(".iso")
}

// ISONotHybrid reports whether the ISO read by r lacks a boot sector (the
// 0x55AA signature closing an MBR): such an ISO (Windows installers, for
// one) only boots from a DVD, not once written onto a USB drive. Images
// that aren't ISOs are never reported, nor an ISO r can't read.
func ISONotHybrid(name string, r io.ReaderAt) bool {
	if !strings.HasSuffix(strings.ToLower(name), ".iso") {
		return false
	}
	var sig [2]byte
	if _, err := r.ReadAt(sig[:], 510); err != nil {
		return false
	}
	return sig != [2]byte{0x55, 0xAA}
}

// ImageProgress is how far WriteImage got: Done of Total bytes, first
// written, then (Verifying) read back.
type ImageProgress struct {
	Verifying   bool
	Done, Total int64
}

var (
	// ErrImageTooBig is (wrapped in) the error of an image larger than
	// the drive.
	ErrImageTooBig = errors.New("image larger than the drive")
	// ErrVerifyMismatch is the error of a drive that doesn't read back
	// what was written onto it.
	ErrVerifyMismatch = errors.New("verify failed: drive data differs")
)
