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

package ui

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/drives"
)

// imageWrite is one writeImageOnto call a test recorded.
type imageWrite struct {
	disk        string
	data        []byte
	verify, ssh bool
	user        *drives.RaspiUser
}

// fakeImageDrives makes the machine show, for one test, the removable
// disks /dev/sdy (two partitions, one mounted, 16 GiB) and /dev/sdz (1
// KiB: too small for any test image), records what's written onto them,
// and extracts images into a folder of the test's own, returned.
func fakeImageDrives(t *testing.T) (*[]imageWrite, string) {
	t.Helper()
	oldList, oldSize, oldWrite, oldDir := listRemovableDrives, diskSizeOf, writeImageOnto, imageStagingDir
	t.Cleanup(func() {
		listRemovableDrives, diskSizeOf, writeImageOnto, imageStagingDir = oldList, oldSize, oldWrite, oldDir
	})
	listRemovableDrives = func() ([]drives.RemovableDevice, error) {
		return []drives.RemovableDevice{
			{Name: "sdy1", Path: "/dev/sdy1", Vendor: "Acme", Model: "Stick", Mounted: true, MountPoint: "/run/media/u/A"},
			{Name: "sdy2", Path: "/dev/sdy2", Vendor: "Acme", Model: "Stick"},
			{Name: "sdz", Path: "/dev/sdz", Model: "Tiny"},
		}, nil
	}
	diskSizeOf = func(disk string) int64 {
		if disk == "/dev/sdy" {
			return 16 << 30
		}
		return 1024
	}
	var writes []imageWrite
	writeImageOnto = func(_ context.Context, disk string, src io.Reader, size int64, opts drives.ImageOptions, progress func(drives.ImageProgress)) error {
		data, err := io.ReadAll(src)
		if err != nil {
			return err
		}
		writes = append(writes, imageWrite{disk, data[:size], opts.Verify, opts.Raspi.SSH, opts.Raspi.User})
		progress(drives.ImageProgress{Done: size, Total: size})
		return nil
	}
	stage := t.TempDir()
	imageStagingDir = func() string { return stage }
	return &writes, stage
}

// isoImage is a small "hybrid" ISO: one with a boot sector, unless plain.
func isoImage(plain bool) []byte {
	b := bytes.Repeat([]byte{0x11}, 4096)
	if !plain {
		b[510], b[511] = 0x55, 0xAA
	}
	return b
}

func pressKey(m *Model, code rune, text string) {
	m.updateDialogKey(tea.KeyPressMsg{Code: code, Text: text})
}

func TestWriteImageLocalISO(t *testing.T) {
	writes, _ := fakeImageDrives(t)
	dir := t.TempDir()
	img := isoImage(false)
	os.WriteFile(filepath.Join(dir, "linux.iso"), img, 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "linux.iso")

	m.askWriteImage()
	d := &m.dialog
	if d.Kind != DialogWriteImage || !d.ImageVerify {
		t.Fatalf("dialog %v (verify %v), want the write image dialog, verifying", d.Kind, d.ImageVerify)
	}
	if len(d.ImageDisks) != 2 || d.ImageDisks[0].Path != "/dev/sdy" || !d.ImageDisks[0].Mounted || d.ImageDisks[1].Unfit != "too small" {
		t.Fatalf("drives %+v, want /dev/sdy (mounted) and /dev/sdz (too small)", d.ImageDisks)
	}
	if strings.Contains(m.renderDialogBox(), "Not a hybrid ISO") {
		t.Error("a hybrid ISO is said not to be")
	}

	// The drive too small can't be chosen.
	d.ItemIdx = 1
	m.confirmDialog()
	if m.dialog.Kind != DialogWriteImage {
		t.Fatalf("went on with a drive too small: dialog %v", m.dialog.Kind)
	}

	m.dialog.ItemIdx = 0
	m.confirmDialog()
	if m.dialog.Kind != DialogWriteImageConfirm {
		t.Fatalf("dialog %v, want the data loss warning", m.dialog.Kind)
	}
	view := m.renderDialogBox()
	for _, want := range []string{"ALL DATA on /dev/sdy", "LOST", "linux.iso", "unmounted"} {
		if !strings.Contains(view, want) {
			t.Errorf("warning doesn't say %q:\n%s", want, view)
		}
	}
	pressKey(m, 'y', "y")
	if m.dialog.Kind != DialogProgress || len(m.tasks) != 1 || m.tasks[0].Kind != TaskWriteImage {
		t.Fatalf("dialog %v, %d task(s): want the image being written", m.dialog.Kind, len(m.tasks))
	}
	if tk := waitTask(t, m); tk.ErrorCount != 0 {
		t.Fatalf("write failed: %v", tk.LastError)
	}
	if len(*writes) != 1 || (*writes)[0].disk != "/dev/sdy" || !bytes.Equal((*writes)[0].data, img) || !(*writes)[0].verify {
		t.Fatalf("writes %+v, want linux.iso onto /dev/sdy, verified", *writes)
	}
	if _, err := os.Stat(filepath.Join(dir, "linux.iso")); err != nil {
		t.Errorf("the image itself is gone: %v", err)
	}
}

func TestWriteImageCancelledByWarning(t *testing.T) {
	writes, _ := fakeImageDrives(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "rpi.img"), isoImage(true), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "rpi.img")
	m.askWriteImage()
	m.confirmDialog()
	pressKey(m, 'n', "n")
	if m.dialog.Kind != DialogNone || len(m.tasks) != 0 || len(*writes) != 0 {
		t.Fatalf("dialog %v, %d task(s), %d write(s): want nothing done", m.dialog.Kind, len(m.tasks), len(*writes))
	}
}

func TestWriteImageWithoutVerify(t *testing.T) {
	writes, _ := fakeImageDrives(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "win.iso"), isoImage(true), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "win.iso")
	m.askWriteImage()
	if !strings.Contains(m.renderDialogBox(), "Not a hybrid ISO") {
		t.Error("an ISO without boot sector isn't warned about")
	}
	pressKey(m, 'v', "v")
	if m.dialog.ImageVerify {
		t.Fatal("v didn't turn verifying off")
	}
	m.confirmDialog()
	m.confirmDialog()
	waitTask(t, m)
	if len(*writes) != 1 || (*writes)[0].verify {
		t.Fatalf("writes %+v, want one not verified", *writes)
	}
}

func TestWriteImageFromArchive(t *testing.T) {
	writes, stage := fakeImageDrives(t)
	dir := t.TempDir()
	img := isoImage(false)
	writeTestZip(t, filepath.Join(dir, "distro.zip"), map[string][]byte{"README": []byte("hi"), "out/distro.iso": img})
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "distro.zip")

	m.askWriteImage()
	if m.dialog.Kind != DialogProgress || m.tasks[0].Kind != TaskStageImage {
		t.Fatalf("dialog %v: want the image being extracted", m.dialog.Kind)
	}
	if tk := waitTask(t, m); tk.ErrorCount != 0 {
		t.Fatalf("extracting failed: %v", tk.LastError)
	}
	if m.dialog.Kind != DialogWriteImage || m.dialog.Image.Name != "distro.iso" {
		t.Fatalf("dialog %v, want the write image dialog for distro.iso", m.dialog.Kind)
	}
	if left, _ := os.ReadDir(stage); len(left) != 1 {
		t.Fatalf("%d file(s) extracted, want the image", len(left))
	}
	m.confirmDialog()
	m.confirmDialog()
	waitTask(t, m)
	if len(*writes) != 1 || !bytes.Equal((*writes)[0].data, img) {
		t.Fatalf("writes %+v, want distro.iso", *writes)
	}
	if left, _ := os.ReadDir(stage); len(left) != 0 {
		t.Errorf("extracted image left behind after writing it")
	}
}

func TestWriteImageExtractedDroppedOnEsc(t *testing.T) {
	_, stage := fakeImageDrives(t)
	dir := t.TempDir()
	writeTestZip(t, filepath.Join(dir, "distro.zip"), map[string][]byte{"distro.img": isoImage(true)})
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "distro.zip")
	m.askWriteImage()
	waitTask(t, m)
	m.confirmDialog() // on to the warning
	m.updateDialogKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.dialog.Kind != DialogNone || m.stagedImage != nil {
		t.Fatalf("dialog %v, staged %v: want all closed", m.dialog.Kind, m.stagedImage)
	}
	if left, _ := os.ReadDir(stage); len(left) != 0 {
		t.Errorf("extracted image left behind after cancelling")
	}
}

func TestWriteImageArchiveWithTwoImages(t *testing.T) {
	writes, stage := fakeImageDrives(t)
	dir := t.TempDir()
	writeTestZip(t, filepath.Join(dir, "two.zip"), map[string][]byte{"a.iso": isoImage(false), "b.iso": isoImage(false)})
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "two.zip")
	m.askWriteImage()
	tk := waitTask(t, m)
	if tk.ErrorCount != 1 || !strings.Contains(tk.LastError.Error(), "more than one image") {
		t.Fatalf("task errors %d (%v), want more than one image", tk.ErrorCount, tk.LastError)
	}
	if m.dialog.Kind == DialogWriteImage || len(*writes) != 0 {
		t.Fatal("went on with an archive of two images")
	}
	if left, _ := os.ReadDir(stage); len(left) != 0 {
		t.Errorf("%d file(s) left behind", len(left))
	}
}

func TestWriteImageDriveSwapped(t *testing.T) {
	writes, _ := fakeImageDrives(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "linux.iso"), isoImage(false), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "linux.iso")
	m.askWriteImage()
	m.confirmDialog()
	// Another stick plugged in its place: same name, other size.
	diskSizeOf = func(string) int64 { return 32 << 30 }
	m.confirmDialog()
	if len(m.tasks) != 0 || len(*writes) != 0 {
		t.Fatal("wrote onto a drive swapped meanwhile")
	}
	if m.dialog.Kind != DialogWriteImage || m.dialog.ImageDisks[0].Size != 32<<30 {
		t.Errorf("dialog %v: want the drives listed again", m.dialog.Kind)
	}
}

func TestWriteImageRefusedWhileTasksRun(t *testing.T) {
	writes, _ := fakeImageDrives(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "linux.iso"), isoImage(false), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "linux.iso")
	m.askWriteImage()
	m.confirmDialog()
	m.tasks = append(m.tasks, &Task{ID: 99, Kind: TaskCopy, Total: 1})
	m.confirmDialog()
	if len(m.tasks) != 1 || len(*writes) != 0 {
		t.Fatal("writing started with a task running")
	}
}

func TestWriteImageNotAnImage(t *testing.T) {
	fakeImageDrives(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "notes.txt")
	m.askWriteImage()
	if m.dialog.Kind != DialogNone || len(m.tasks) != 0 {
		t.Fatalf("dialog %v, %d task(s) for a text file", m.dialog.Kind, len(m.tasks))
	}
}

// writeTestZip writes a ZIP of files (name → content) at p.
func writeTestZip(t *testing.T, p string, files map[string][]byte) {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeRaspiOS makes, for one test, every image whose name says so taken
// for Raspberry Pi OS.
func fakeRaspiOS(t *testing.T) {
	old := isRaspiOSImage
	t.Cleanup(func() { isRaspiOSImage = old })
	isRaspiOSImage = func(name string, _ io.ReaderAt) bool { return strings.Contains(name, "raspios") }
}

func TestWriteImageRaspiOSEnablesSSH(t *testing.T) {
	writes, _ := fakeImageDrives(t)
	fakeRaspiOS(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "raspios-lite.img"), isoImage(true), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "raspios-lite.img")
	m.askWriteImage()
	if !m.dialog.ImageSSH {
		t.Fatal("SSH not on by default for Raspberry Pi OS")
	}
	if view := m.renderDialogBox(); !strings.Contains(view, "[x] Enable SSH") {
		t.Errorf("no SSH option checked:\n%s", view)
	}
	m.confirmDialog()
	if view := m.renderDialogBox(); !strings.Contains(view, "SSH will be enabled") {
		t.Errorf("warning doesn't say SSH will be enabled:\n%s", view)
	}
	m.confirmDialog()
	waitTask(t, m)
	if len(*writes) != 1 || !(*writes)[0].ssh {
		t.Fatalf("writes %+v, want one enabling SSH", *writes)
	}
	if !strings.Contains(m.status, "SSH enabled") {
		t.Errorf("status %q doesn't say SSH was enabled", m.status)
	}
}

func TestWriteImageRaspiOSWithoutSSH(t *testing.T) {
	writes, _ := fakeImageDrives(t)
	fakeRaspiOS(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "raspios-lite.img"), isoImage(true), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "raspios-lite.img")
	m.askWriteImage()
	pressKey(m, 's', "s")
	if m.dialog.ImageSSH || !strings.Contains(m.renderDialogBox(), "[ ] Enable SSH") {
		t.Fatal("s didn't turn SSH off")
	}
	m.confirmDialog()
	if strings.Contains(m.renderDialogBox(), "SSH will be enabled") {
		t.Error("warning says SSH will be enabled though turned off")
	}
	m.confirmDialog()
	waitTask(t, m)
	if len(*writes) != 1 || (*writes)[0].ssh {
		t.Fatalf("writes %+v, want one not enabling SSH", *writes)
	}
}

// Other images have no SSH option, and s does nothing to them.
func TestWriteImageNoSSHForOtherImages(t *testing.T) {
	writes, _ := fakeImageDrives(t)
	fakeRaspiOS(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "linux.iso"), isoImage(false), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "linux.iso")
	m.askWriteImage()
	pressKey(m, 's', "s")
	if m.dialog.ImageSSH || strings.Contains(m.renderDialogBox(), "SSH") {
		t.Fatal("SSH offered for an image that isn't Raspberry Pi OS")
	}
	m.confirmDialog()
	m.confirmDialog()
	waitTask(t, m)
	if len(*writes) != 1 || (*writes)[0].ssh {
		t.Fatalf("writes %+v, want one not enabling SSH", *writes)
	}
}

// typeKeys types text, key by key, into the dialog's focused field.
func typeKeys(m *Model, text string) {
	for _, r := range text {
		pressKey(m, r, string(r))
	}
}

func TestWriteImageRaspiOSCreatesUser(t *testing.T) {
	writes, _ := fakeImageDrives(t)
	fakeRaspiOS(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "raspios-lite.img"), isoImage(true), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "raspios-lite.img")
	m.askWriteImage()
	if !strings.Contains(m.renderDialogBox(), "[ ] Create a user") {
		t.Fatalf("no user option, unchecked:\n%s", m.renderDialogBox())
	}
	pressKey(m, 'u', "u")
	if m.dialog.Kind != DialogWriteImageUser {
		t.Fatalf("dialog %v, want the user form", m.dialog.Kind)
	}
	if got := m.dialog.Inputs[0].Value(); got != "pi" {
		t.Fatalf("user name %q, want pi by default", got)
	}
	typeKeys(m, "-jk")
	pressKey(m, tea.KeyTab, "")
	typeKeys(m, "secret")
	pressKey(m, tea.KeyTab, "")
	typeKeys(m, "secreT")
	m.confirmDialog()
	if m.dialog.Kind != DialogWriteImageUser || !strings.Contains(m.renderDialogBox(), "Passwords differ") {
		t.Fatalf("different passwords accepted: dialog %v", m.dialog.Kind)
	}
	if strings.Contains(m.renderDialogBox(), "secret") {
		t.Error("password shown in clear")
	}
	pressKey(m, tea.KeyBackspace, "")
	typeKeys(m, "t")
	m.confirmDialog()
	d := &m.dialog
	if d.Kind != DialogWriteImage || d.ImageUser == nil || d.ImageUser.Name != "pi-jk" || !strings.HasPrefix(d.ImageUser.PasswordHash, "$6$") {
		t.Fatalf("dialog %v, user %+v: want back to the drives with pi-jk", d.Kind, d.ImageUser)
	}
	if view := m.renderDialogBox(); !strings.Contains(view, "[x] Create the user") || !strings.Contains(view, "pi-jk") {
		t.Errorf("user not shown:\n%s", view)
	}
	m.confirmDialog()
	if view := m.renderDialogBox(); !strings.Contains(view, "pi-jk will be created") {
		t.Errorf("warning doesn't name the user:\n%s", view)
	}
	m.confirmDialog()
	waitTask(t, m)
	if len(*writes) != 1 || (*writes)[0].user == nil || (*writes)[0].user.Name != "pi-jk" || !(*writes)[0].ssh {
		t.Fatalf("writes %+v, want one creating pi-jk, SSH on", *writes)
	}
	if !strings.Contains(m.status, "user pi-jk set") {
		t.Errorf("status %q doesn't name the user", m.status)
	}
}

// Esc leaves the form, keeping the image and drives; u on a user set
// removes it; a name Raspberry Pi OS refuses is refused.
func TestWriteImageRaspiOSUserFormEsc(t *testing.T) {
	writes, _ := fakeImageDrives(t)
	fakeRaspiOS(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "raspios-lite.img"), isoImage(true), 0o644)
	m := archiveTestModel(t, dir)
	moveCursorTo(t, m.panes[0], "raspios-lite.img")
	m.askWriteImage()
	pressKey(m, 'u', "u")
	pressKey(m, tea.KeyEscape, "")
	if m.dialog.Kind != DialogWriteImage || m.dialog.ImageUser != nil || len(m.dialog.ImageDisks) != 2 {
		t.Fatalf("dialog %v, user %v: want back to the drives, no user", m.dialog.Kind, m.dialog.ImageUser)
	}
	pressKey(m, 'u', "u")
	m.dialog.Inputs[0].SetValue("root")
	pressKey(m, tea.KeyTab, "")
	typeKeys(m, "x")
	pressKey(m, tea.KeyTab, "")
	typeKeys(m, "x")
	m.confirmDialog()
	if m.dialog.Kind != DialogWriteImageUser || !strings.Contains(m.renderDialogBox(), "root") {
		t.Fatalf("root accepted: dialog %v", m.dialog.Kind)
	}
	m.dialog.Inputs[0].SetValue("pi")
	m.confirmDialog()
	if m.dialog.ImageUser == nil {
		t.Fatal("pi refused")
	}
	pressKey(m, 'u', "u")
	if m.dialog.Kind != DialogWriteImage || m.dialog.ImageUser != nil {
		t.Fatal("u didn't remove the user")
	}
	m.confirmDialog()
	m.confirmDialog()
	waitTask(t, m)
	if len(*writes) != 1 || (*writes)[0].user != nil {
		t.Fatalf("writes %+v, want one creating no user", *writes)
	}
}
