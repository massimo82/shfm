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

//go:build linux

package drives

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

const (
	imageChunk     = 4 << 20  // bytes written or read back at a time: a multiple of any block size
	imageSyncEvery = 32 << 20 // written bytes between two fdatasync, so progress follows the drive
)

// DiskSize returns the size in bytes of the whole disk wholeDiskPath (e.g.
// "/dev/sdb"), as sysfs reports it; 0 when unknown.
func DiskSize(wholeDiskPath string) int64 {
	name := strings.TrimPrefix(wholeDiskPath, "/dev/")
	sectors, err := strconv.ParseInt(readSysfsTrim(filepath.Join("/sys/block", name, "size")), 10, 64)
	if err != nil {
		return 0
	}
	return sectors * 512 // sysfs counts 512-byte sectors whatever the drive's own
}

// WriteImage writes the size bytes of src onto the whole disk
// wholeDiskPath, byte by byte as dd does, replacing everything on it, and
// then, as opts asks, reads them back to check the drive holds them and
// sets up the Raspberry Pi OS written (SSH, user). progress, when not nil, is
// called after every chunk.
//
// Every filesystem on the disk is unmounted first. The disk is then
// opened through udisks2 (Block.OpenDevice, exclusively: it fails if the
// disk is still in use), which asks polkit, so the user's password, as
// GNOME Disks' "Restore Disk Image" does: shfm needs no root. Refused
// for the system disk, and for an image larger than the disk before
// anything is written.
func WriteImage(ctx context.Context, wholeDiskPath string, src io.Reader, size int64, opts ImageOptions, progress func(ImageProgress)) error {
	if IsSystemDisk(wholeDiskPath) {
		return fmt.Errorf("%s holds the system", wholeDiskPath)
	}
	if disk := DiskSize(wholeDiskPath); disk > 0 && size > disk {
		return ErrImageTooBig
	}
	if err := unmountDisk(wholeDiskPath); err != nil {
		return err
	}
	f, err := openDeviceViaUDisks2(ctx, wholeDiskPath)
	if err != nil {
		return err
	}
	defer f.Close()
	// The opened device's own size, should sysfs have said nothing.
	if end, err := f.Seek(0, io.SeekEnd); err == nil && end > 0 && size > end {
		return ErrImageTooBig
	}
	return writeImage(ctx, f, src, size, opts, progress)
}

// openDeviceViaUDisks2 opens the block device devicePath for reading and
// writing, exclusively, through udisks2.
func openDeviceViaUDisks2(ctx context.Context, devicePath string) (*os.File, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("cannot reach the system D-Bus: %w", err)
	}
	defer conn.Close() // the descriptor received stays open

	call := conn.Object(udisksService, blockObjectPath(devicePath)).CallWithContext(ctx,
		udisksService+".Block.OpenDevice", 0, "rw", map[string]dbus.Variant{
			"flags": dbus.MakeVariant(int32(unix.O_EXCL | unix.O_CLOEXEC)),
		})
	if call.Err != nil {
		return nil, udisksError(call.Err)
	}
	var fd dbus.UnixFD
	if err := call.Store(&fd); err != nil {
		return nil, fmt.Errorf("udisks2: unexpected reply: %w", err)
	}
	return os.NewFile(uintptr(fd), devicePath), nil
}

// writeImage writes the size bytes of src at the start of dst and syncs
// them, then verifies them and sets up Raspberry Pi OS when asked (see
// WriteImage).
func writeImage(ctx context.Context, dst *os.File, src io.Reader, size int64, opts ImageOptions, progress func(ImageProgress)) error {
	buf := make([]byte, imageChunk)
	h := sha256.New()
	var done, unsynced int64
	for done < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(int64(len(buf)), size-done)
		if _, err := io.ReadFull(src, buf[:n]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return errors.New("image shorter than expected")
			}
			return err
		}
		if _, err := dst.WriteAt(buf[:n], done); err != nil {
			if errors.Is(err, unix.ENOSPC) {
				return ErrImageTooBig
			}
			return err
		}
		h.Write(buf[:n])
		done += n
		if unsynced += n; unsynced >= imageSyncEvery {
			if err := unix.Fdatasync(int(dst.Fd())); err != nil {
				return err
			}
			unsynced = 0
		}
		if progress != nil {
			progress(ImageProgress{Done: done, Total: size})
		}
	}
	if err := dst.Sync(); err != nil {
		return err
	}
	if opts.Verify {
		if err := verifyImage(ctx, dst, size, h.Sum(nil), progress); err != nil {
			return err
		}
	}
	if opts.Raspi.Empty() {
		return nil
	}
	// Once verified: the files change the drive from the image.
	if err := SetupRaspiOS(dst, opts.Raspi, time.Now()); err != nil {
		return fmt.Errorf("written, but SSH/user not set: %w", err)
	}
	return dst.Sync()
}

// verifyImage reads back the first size bytes of f and checks their
// SHA-256 is want. The reads bypass the page cache (O_DIRECT, when f
// takes it, after dropping what's cached), so they come from the drive
// itself rather than from the memory that wrote them.
func verifyImage(ctx context.Context, f *os.File, size int64, want []byte, progress func(ImageProgress)) error {
	fd := f.Fd()
	_ = unix.Fadvise(int(fd), 0, 0, unix.FADV_DONTNEED)
	flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	direct := err == nil
	if direct {
		if _, err := unix.FcntlInt(fd, unix.F_SETFL, flags|unix.O_DIRECT); err != nil {
			direct = false
		} else {
			// Writes after it (SetupRaspiOS's) are neither aligned
			// nor sized for O_DIRECT.
			defer unix.FcntlInt(fd, unix.F_SETFL, flags)
		}
	}
	// O_DIRECT reads need a buffer aligned to the block size: mmap's
	// memory is page-aligned.
	buf, err := unix.Mmap(-1, 0, imageChunk, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return err
	}
	defer unix.Munmap(buf)

	h := sha256.New()
	var done int64
	for done < size {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := min(int64(len(buf)), size-done)
		read := n
		if direct {
			read = min((n+4095)&^4095, int64(len(buf)))
		}
		got, err := f.ReadAt(buf[:read], done)
		if direct && errors.Is(err, unix.EINVAL) {
			// The filesystem or driver refuses direct reads after all:
			// read through the cache, emptied above.
			if _, err := unix.FcntlInt(fd, unix.F_SETFL, flags); err != nil {
				return err
			}
			direct = false
			continue
		}
		if int64(got) < n {
			if err == nil || errors.Is(err, io.EOF) {
				return ErrVerifyMismatch
			}
			return err
		}
		h.Write(buf[:n])
		done += n
		if progress != nil {
			progress(ImageProgress{Verifying: true, Done: done, Total: size})
		}
	}
	if !bytes.Equal(h.Sum(nil), want) {
		return ErrVerifyMismatch
	}
	return nil
}
