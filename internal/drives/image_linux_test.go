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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDrive is a file standing in for the drive, already holding old
// data past where the image ends.
func fakeDrive(t *testing.T, size int) *os.File {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(t.TempDir(), "drive"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if _, err := f.Write(bytes.Repeat([]byte{0xEE}, size)); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestWriteImageWritesAndVerifies(t *testing.T) {
	img := testImage(2*imageChunk + 777)
	f := fakeDrive(t, len(img)+4096)
	var last ImageProgress
	verified := false
	err := writeImage(context.Background(), f, bytes.NewReader(img), int64(len(img)), ImageOptions{Verify: true}, func(p ImageProgress) {
		last = p
		verified = verified || p.Verifying
	})
	if err != nil {
		t.Fatal(err)
	}
	if !verified || last.Done != int64(len(img)) || last.Total != int64(len(img)) {
		t.Errorf("last progress %+v (verified %v), want all %d bytes verified", last, verified, len(img))
	}
	got, _ := os.ReadFile(f.Name())
	if !bytes.Equal(got[:len(img)], img) {
		t.Error("drive doesn't hold the image")
	}
	if !bytes.Equal(got[len(img):], bytes.Repeat([]byte{0xEE}, 4096)) {
		t.Error("bytes past the image were touched")
	}
}

func TestWriteImageWithoutVerify(t *testing.T) {
	img := testImage(imageChunk + 5)
	f := fakeDrive(t, len(img))
	err := writeImage(context.Background(), f, bytes.NewReader(img), int64(len(img)), ImageOptions{}, func(p ImageProgress) {
		if p.Verifying {
			t.Error("verified though not asked")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWriteImageShortSource(t *testing.T) {
	img := testImage(1000)
	f := fakeDrive(t, 4096)
	err := writeImage(context.Background(), f, bytes.NewReader(img), 2000, ImageOptions{Verify: true}, nil)
	if err == nil {
		t.Fatal("an image shorter than its size was accepted")
	}
}

func TestWriteImageCancelled(t *testing.T) {
	img := testImage(3 * imageChunk)
	f := fakeDrive(t, len(img))
	ctx, cancel := context.WithCancel(context.Background())
	err := writeImage(ctx, f, bytes.NewReader(img), int64(len(img)), ImageOptions{Verify: true}, func(ImageProgress) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestVerifyImageMismatch(t *testing.T) {
	img := testImage(imageChunk + 3000)
	f := fakeDrive(t, len(img))
	if _, err := f.WriteAt(img, 0); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(img)
	if err := verifyImage(context.Background(), f, int64(len(img)), want[:], nil); err != nil {
		t.Fatalf("intact copy: %v", err)
	}
	// One byte changed on the "drive", as a failing stick would.
	if _, err := f.WriteAt([]byte{img[imageChunk+10] ^ 0xFF}, imageChunk+10); err != nil {
		t.Fatal(err)
	}
	if err := verifyImage(context.Background(), f, int64(len(img)), want[:], nil); !errors.Is(err, ErrVerifyMismatch) {
		t.Fatalf("corrupted copy: got %v, want ErrVerifyMismatch", err)
	}
}

func TestVerifyImageDriveTooShort(t *testing.T) {
	img := testImage(5000)
	f := fakeDrive(t, 4000)
	want := sha256.Sum256(img)
	if err := verifyImage(context.Background(), f, int64(len(img)), want[:], nil); !errors.Is(err, ErrVerifyMismatch) {
		t.Fatalf("got %v, want ErrVerifyMismatch", err)
	}
}

// WriteImage refuses the system disk before unmounting or opening it.
func TestWriteImageRefusesSystemDisk(t *testing.T) {
	disk := fakeSystemDisk(t)
	err := WriteImage(context.Background(), disk, bytes.NewReader(nil), 0, ImageOptions{}, nil)
	if err == nil || !strings.Contains(err.Error(), "holds the system") {
		t.Fatalf("WriteImage(system disk) = %v; want a refusal", err)
	}
}

// SSH and the user are set up once the image is verified, on the drive only.
func TestWriteImageEnablesSSH(t *testing.T) {
	img := buildFATImage(t, fatSpec{bits: 32, issue: piIssue})
	before := bytes.Clone(img)
	f := fakeDrive(t, len(img)+4096)
	if err := writeImage(context.Background(), f, bytes.NewReader(img), int64(len(img)), ImageOptions{Verify: true, Raspi: RaspiSetup{SSH: true, User: &testUser}}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(f.Name())
	if !rootHas(t, memDisk(got), "SSH        ") {
		t.Fatal("no ssh file on the drive")
	}
	if got := userconf(t, memDisk(got), "USERCONFTXT"); got != testUserLine {
		t.Errorf("userconf.txt %q, want %q", got, testUserLine)
	}
	if !bytes.Equal(img, before) {
		t.Error("the image itself changed")
	}
}

func TestWriteImageSSHNotEnabledReported(t *testing.T) {
	img := testImage(64 << 10)
	f := fakeDrive(t, len(img))
	err := writeImage(context.Background(), f, bytes.NewReader(img), int64(len(img)), ImageOptions{Raspi: RaspiSetup{SSH: true}}, nil)
	if err == nil || !strings.Contains(err.Error(), "SSH/user not set") {
		t.Fatalf("got %v, want SSH/user not set", err)
	}
}
