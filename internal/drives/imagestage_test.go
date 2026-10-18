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
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"shfm/internal/archive"
)

type stageFile struct {
	name string
	data []byte
}

func writeZip(t *testing.T, p string, files ...stageFile) {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(f.data)
	}
	zw.Close()
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeTarGz(t *testing.T, p string, files ...stageFile) {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		tw.Write(f.data)
	}
	tw.Close()
	gz.Close()
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// stage runs StageImage on the archive at p, extracting into a folder of
// its own, and returns that folder's entries along with the result.
func stage(t *testing.T, p string) (StagedImage, []os.DirEntry, error) {
	t.Helper()
	dir := t.TempDir()
	img, err := StageImage(context.Background(), archive.Source{Name: p, LocalPath: p}, dir, nil)
	left, _ := os.ReadDir(dir)
	return img, left, err
}

func TestStageImageFromTarGz(t *testing.T) {
	img := testImage(300000)
	p := filepath.Join(t.TempDir(), "distro.tar.gz")
	writeTarGz(t, p, stageFile{"README", []byte("hi")}, stageFile{"dir/distro.iso", img}, stageFile{"SHA256SUMS", []byte("x")})
	staged, _, err := stage(t, p)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(staged.Path)
	if staged.Size != int64(len(img)) || !bytes.Equal(got, img) {
		t.Errorf("staged %d bytes, want the %d of the image", staged.Size, len(img))
	}
	if staged.Name != "distro.iso" {
		t.Errorf("staged image named %q, want distro.iso", staged.Name)
	}
}

func TestStageImageFromSingleGzip(t *testing.T) {
	img := testImage(70000)
	dir := t.TempDir()
	p := filepath.Join(dir, "raspios.img.gz")
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	gz.Write(img)
	gz.Close()
	os.WriteFile(p, b.Bytes(), 0o600)
	staged, _, err := stage(t, p)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(staged.Path); !bytes.Equal(got, img) {
		t.Error("staged file isn't the image")
	}
}

func TestStageImageRefusesSeveralImages(t *testing.T) {
	p := filepath.Join(t.TempDir(), "two.zip")
	writeZip(t, p, stageFile{"a.iso", testImage(5000)}, stageFile{"b.img", testImage(6000)})
	_, left, err := stage(t, p)
	if !errors.Is(err, ErrManyImages) {
		t.Fatalf("got %v, want ErrManyImages", err)
	}
	if len(left) != 0 {
		t.Errorf("%d file(s) left behind", len(left))
	}
}

func TestStageImageRefusesNoImage(t *testing.T) {
	p := filepath.Join(t.TempDir(), "docs.zip")
	writeZip(t, p, stageFile{"notes.txt", []byte("x")}, stageFile{"iso/readme", []byte("y")})
	if _, _, err := stage(t, p); !errors.Is(err, ErrNoImage) {
		t.Fatalf("got %v, want ErrNoImage", err)
	}
}

func TestStageImageRefusesFullFolder(t *testing.T) {
	p := filepath.Join(t.TempDir(), "distro.zip")
	writeZip(t, p, stageFile{"distro.iso", testImage(5000)})
	defer func(old func(string) (uint64, uint64)) { diskUsageOf = old }(diskUsageOf)
	// A 10 GiB filesystem with 600 MiB free: already under its reserve
	// (a tenth of it), so no room for anything.
	diskUsageOf = func(string) (uint64, uint64) { return 10 << 30, 600 << 20 }
	_, left, err := stage(t, p)
	if !errors.Is(err, ErrStageNoSpace) {
		t.Fatalf("got %v, want ErrStageNoSpace", err)
	}
	if len(left) != 0 {
		t.Errorf("%d file(s) left behind", len(left))
	}
}

func TestStageFileCopiesAndCleansUp(t *testing.T) {
	img := testImage(200000)
	dir := t.TempDir()
	staged, err := StageFile(context.Background(), bytes.NewReader(img), "remote.iso", dir, int64(len(img)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(staged.Path); staged.Size != int64(len(img)) || !bytes.Equal(got, img) {
		t.Error("staged copy isn't the image")
	}
	os.Remove(staged.Path)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := StageFile(ctx, bytes.NewReader(img), "remote.iso", dir, -1, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled copy: got %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("%d file(s) left behind by a cancelled copy", len(left))
	}
}
