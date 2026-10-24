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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textinput"

	"shfm/internal/applog"
	"shfm/internal/archive"
	"shfm/internal/config"
	"shfm/internal/drives"
	"shfm/internal/fileops"
	"shfm/internal/vfs"
)

// --- write a disk image onto a removable drive ----------------------------------

// The image writer's views of the machine and its actions on it:
// variables, so that tests never touch a real drive.
var (
	diskSizeOf      = drives.DiskSize
	writeImageOnto  = drives.WriteImage
	stageImage      = drives.StageImage
	stageImageFile  = drives.StageFile
	imageStagingDir = os.TempDir
	isRaspiOSImage  = drives.IsRaspiOSImage // tests fake it: their images hold no filesystem
)

// diskImage is an image ready to be written: always a local file, its own
// or, extracted from an archive or copied from a network source, a
// temporary one (Staged) removed once done with.
type diskImage struct {
	Name      string // the image's own name, for display
	Path      string // local file
	Size      int64
	Staged    bool
	NotHybrid bool // an ISO that won't boot from a USB drive (see drives.ISONotHybrid)
	RaspiOS   bool // Raspberry Pi OS: SSH can be turned on (see drives.IsRaspiOSImage)

	// Where it comes from, so pressing the key again on that file finds
	// the image already extracted.
	originFS   vfs.FileSystem
	originPath string
}

// drop removes the image's temporary file, if it has one.
func (img *diskImage) drop() {
	if img != nil && img.Staged {
		if err := os.Remove(img.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			applog.Warn("could not remove the extracted image", "path", img.Path, "error", err)
		}
	}
}

// imageDisk is a removable disk offered as the image's destination.
type imageDisk struct {
	Path    string // whole disk, e.g. "/dev/sdb"
	Name    string // vendor and model, "" when unknown
	Size    int64  // 0 when unknown
	Mounted bool   // some filesystem of it is mounted (it will be unmounted)
	// Why it can't take the image, "" when it can.
	Unfit string
}

// DropStagedImage removes the image extracted for writing, if one is
// still waiting: called once shfm exits.
func (m *Model) DropStagedImage() {
	if m.stagedImage != nil {
		m.stagedImage.drop()
		m.stagedImage = nil
	}
}

// keepStagedImage makes img the image waiting to be written, dropping the
// one it replaces.
func (m *Model) keepStagedImage(img *diskImage) {
	if m.stagedImage != nil && m.stagedImage != img {
		m.stagedImage.drop()
	}
	m.stagedImage = img
}

// askWriteImage starts writing the file under the cursor (or the one
// selected) onto a removable drive: a disk image (ISO, IMG) on a local
// filesystem opens the dialog choosing the drive at once; one inside an
// archive is first extracted into the temporary folder, as is one on a
// network source copied there, in the background.
func (m *Model) askWriteImage() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	names := p.SelectedNames()
	if len(names) != 1 {
		m.setStatus("Select one disk image to write")
		return
	}
	name := names[0]
	e, ok := p.entryByName(name)
	if !ok || e.IsDir {
		m.setStatus("Not a disk image (ISO, IMG)")
		return
	}
	full := p.FS.Join(p.Path, name)
	if img := m.stagedImage; img != nil && img.originFS == p.FS && img.originPath == full {
		m.openWriteImage(img)
		return
	}
	local := ""
	if lp, ok := p.FS.(vfs.LocalPath); ok {
		local, _ = lp.LocalPath(full)
	}
	kind, _ := archive.Detect(name)
	switch {
	case drives.IsImageName(name) && local != "":
		m.openWriteImage(&diskImage{Name: name, Path: local, Size: e.Size, originFS: p.FS, originPath: full})
	case drives.IsImageName(name):
		m.startStageImage(p.FS, full, name, e.Size, nil)
	case kind != "" && archiveReadNeeds(kind) != "":
		m.setError("Can't read %s: install %s", name, archiveReadNeeds(kind))
	case kind != "":
		src := archive.Source{Name: name, LocalPath: local, TempDir: imageStagingDir()}
		if local == "" {
			fs := p.FS
			src.Open = func() (io.ReadCloser, error) { return fs.Open(full) }
		}
		m.startStageImage(p.FS, full, name, e.Size, &src)
	default:
		m.setStatus("Not a disk image (ISO, IMG) nor an archive")
	}
}

// startStageImage extracts the image the archive src holds (src nil: copies
// the image itself, full on fs) into the temporary folder, in the
// background, then opens the dialog choosing the drive.
func (m *Model) startStageImage(fs vfs.FileSystem, full, name string, size int64, src *archive.Source) {
	var img *diskImage
	t := m.startTask(TaskStageImage, 1, func(prog *fileops.Progress) *fileops.Result {
		ctx, stop := progressContext(prog)
		defer stop()
		report := throttledBytes(prog)
		onBytes := func(done, total int64) { report(fileops.Bytes{Done: done, Total: total, FilesTotal: 1}, false) }
		dir := imageStagingDir()
		var staged drives.StagedImage
		var err error
		if src != nil {
			staged, err = stageImage(ctx, *src, dir, onBytes)
		} else {
			var rc io.ReadCloser
			if rc, err = fs.Open(full); err == nil {
				staged, err = stageImageFile(ctx, rc, name, dir, size, onBytes)
				rc.Close()
			}
		}
		res := &fileops.Result{}
		switch {
		case ctx.Err() != nil:
			res.Cancelled = true
		case err != nil:
			res.Errors = []error{err}
			prog.OnItem(0, 1, name, err)
		default:
			res.Done = 1
			img = &diskImage{Name: staged.Name, Path: staged.Path, Size: staged.Size, Staged: true, originFS: fs, originPath: full}
			report(fileops.Bytes{Done: staged.Size, Total: staged.Size, Files: 1, FilesTotal: 1}, true)
			prog.OnItem(1, 1, staged.Name, nil)
		}
		return res
	})
	t.Label = name
	t.onFinish = func(t *Task) {
		if img == nil {
			return
		}
		m.keepStagedImage(img)
		// Opened over this task's own progress, or when nothing else is
		// shown; otherwise the user is busy: the image waits for them.
		if m.dialog.Kind == DialogNone || m.isTaskForegrounded(t) {
			m.openWriteImage(img)
		} else {
			m.setStatus("%s extracted: press %s on %s again to write it", img.Name, m.firstKey(config.ActionWriteImage), name)
		}
	}
	m.dialog = Dialog{Kind: DialogProgress, Title: "Extracting image", TaskID: t.ID}
}

// openWriteImage opens the dialog choosing the drive img is written onto.
func (m *Model) openWriteImage(img *diskImage) {
	if f, err := os.Open(img.Path); err == nil {
		img.NotHybrid = drives.ISONotHybrid(img.Name, f)
		img.RaspiOS = isRaspiOSImage(img.Name, f)
		f.Close()
	}
	d := Dialog{Kind: DialogWriteImage, Title: "Write image", Image: img, ImageVerify: true, ImageSSH: img.RaspiOS}
	m.dialog = d
	m.refreshImageDisks()
}

// refreshImageDisks lists, in the write image dialog, the removable disks
// the image can go onto (never the system's), keeping the one selected.
func (m *Model) refreshImageDisks() {
	d := &m.dialog
	selected := ""
	if d.ItemIdx >= 0 && d.ItemIdx < len(d.ImageDisks) {
		selected = d.ImageDisks[d.ItemIdx].Path
	}
	imgDisk := ""
	if mt, ok := mountOf(d.Image.Path); ok {
		imgDisk = drives.WholeDiskDevicePath(mt.Device)
	}
	removable, _ := listRemovableDrives()
	var disks []imageDisk
	index := map[string]int{}
	for _, r := range removable {
		path := drives.WholeDiskDevicePath(r.Path)
		if drives.IsSystemDisk(path) {
			continue
		}
		i, ok := index[path]
		if !ok {
			i = len(disks)
			index[path] = i
			disks = append(disks, imageDisk{Path: path, Name: strings.TrimSpace(driveDisplayName(r.Vendor, r.Model)), Size: diskSizeOf(path)})
		}
		disks[i].Mounted = disks[i].Mounted || r.Mounted
	}
	d.ImageDisks = disks
	d.Items = make([]string, len(disks))
	d.ItemIdx = 0
	for i := range disks {
		dk := &disks[i]
		switch {
		case dk.Path == imgDisk:
			dk.Unfit = "holds the image"
		case dk.Size > 0 && d.Image.Size > dk.Size:
			dk.Unfit = "too small"
		}
		label := dk.Path
		if dk.Name != "" {
			label += "  " + dk.Name
		}
		if dk.Size > 0 {
			label += "  (" + humanSize(dk.Size) + ")"
		}
		if dk.Unfit != "" {
			label += " — " + dk.Unfit
		}
		d.Items[i] = label
		if dk.Path == selected {
			d.ItemIdx = i
		}
	}
}

// updateWriteImageKey handles the write image dialog's own keys: v/Space
// turn verifying on and off, s enabling SSH and u creating a user
// (Raspberry Pi OS only), r lists the drives again; Esc drops the image
// extracted for it.
func (m *Model) updateWriteImageKey(key string) bool {
	d := &m.dialog
	switch key {
	case "v", "space":
		d.ImageVerify = !d.ImageVerify
	case "s":
		if !d.Image.RaspiOS {
			return false
		}
		d.ImageSSH = !d.ImageSSH
	case "u":
		if !d.Image.RaspiOS {
			return false
		}
		if d.ImageUser != nil {
			d.ImageUser = nil
		} else {
			m.askWriteImageUser()
		}
	case "r":
		m.refreshImageDisks()
	case "esc":
		m.dialog = Dialog{}
		m.DropStagedImage()
	default:
		return false
	}
	return true
}

// askWriteImageUser asks, over the write image dialog, for the user
// Raspberry Pi OS creates on first boot: pi, its name of old, unless
// changed.
func (m *Model) askWriteImageUser() {
	d := &m.dialog
	placeholders := []string{"lower case: a-z 0-9 -", "not empty", "the same again"}
	d.Inputs = make([]textinput.Model, len(placeholders))
	for i, p := range placeholders {
		ti := textinput.New()
		ti.Placeholder = p
		ti.SetWidth(36)
		ti.CharLimit = 255
		if i > 0 {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '•'
		}
		d.Inputs[i] = ti
	}
	d.Inputs[0].CharLimit = 32
	d.Inputs[0].SetValue("pi")
	d.Inputs[0].CursorEnd()
	d.Inputs[0].Focus()
	d.FocusIdx, d.Message, d.IsError = 0, "", false
	d.Kind, d.Title = DialogWriteImageUser, "Create a user"
}

// confirmWriteImageUser takes the user asked for, its password kept
// hashed only, back to the write image dialog; or says what's wrong.
func (m *Model) confirmWriteImageUser() {
	d := &m.dialog
	name, pw, again := d.Inputs[0].Value(), d.Inputs[1].Value(), d.Inputs[2].Value()
	if pw != again {
		d.Message, d.IsError = "Passwords differ", true
		return
	}
	u, err := drives.NewRaspiUser(name, pw)
	if err != nil {
		d.Message, d.IsError = err.Error(), true
		return
	}
	d.ImageUser = &u
	m.closeWriteImageUser()
}

// closeWriteImageUser goes back to the write image dialog.
func (m *Model) closeWriteImageUser() {
	d := &m.dialog
	d.Inputs, d.FocusIdx, d.Message, d.IsError = nil, 0, "", false
	d.Kind, d.Title = DialogWriteImage, "Write image"
}

// confirmWriteImageDisk moves from the drive chosen to the warning that
// everything on it will be lost.
func (m *Model) confirmWriteImageDisk() {
	d := m.dialog
	if d.ItemIdx < 0 || d.ItemIdx >= len(d.ImageDisks) {
		return // no drive: stay, r lists them again
	}
	if dk := d.ImageDisks[d.ItemIdx]; dk.Unfit != "" {
		m.setError("%s %s", dk.Path, dk.Unfit)
		return
	}
	d.Kind, d.Title = DialogWriteImageConfirm, "Data will be lost"
	m.dialog = d
}

// writeImage writes the image onto the drive confirmed, in the background.
// Refused while another task runs, as it may be using the drive. Panes
// browsing the drive go back to the home folder: its filesystems are
// about to be replaced.
func (m *Model) writeImage() {
	d := m.dialog
	img, dk := d.Image, d.ImageDisks[d.ItemIdx]
	if m.hasRunningTasks() {
		m.setError("Tasks running: wait for them before writing")
		return
	}
	// The drive may have been swapped since it was listed, another one
	// getting its name.
	if !m.imageDiskStillThere(dk) {
		m.setError("%s changed: choose the drive again", dk.Path)
		m.dialog.Kind, m.dialog.Title = DialogWriteImage, "Write image"
		m.refreshImageDisks()
		return
	}
	m.dialog = Dialog{}
	if m.stagedImage == img {
		m.stagedImage = nil // the task drops it now
	}
	home := homeOrRoot()
	for _, idx := range m.panesOnDisk(dk.Path) {
		m.replaceFS(idx, vfs.NewLocalFS("Local", home), home)
	}
	opts := drives.ImageOptions{Verify: d.ImageVerify}
	if img.RaspiOS {
		opts.Raspi = drives.RaspiSetup{SSH: d.ImageSSH, User: d.ImageUser}
	}
	t := m.startTask(TaskWriteImage, 1, func(prog *fileops.Progress) *fileops.Result {
		defer img.drop()
		ctx, stop := progressContext(prog)
		defer stop()
		res := &fileops.Result{}
		f, err := os.Open(img.Path)
		if err == nil {
			report := throttledBytes(prog)
			verifying := false
			err = writeImageOnto(ctx, dk.Path, f, img.Size, opts, func(p drives.ImageProgress) {
				if p.Verifying && !verifying {
					verifying = true
					report(fileops.Bytes{Done: p.Done, Total: p.Total, FilesTotal: 1}, true)
					prog.OnItem(0, 1, "verifying "+dk.Path, nil)
					return
				}
				report(fileops.Bytes{Done: p.Done, Total: p.Total, FilesTotal: 1}, p.Done == p.Total)
			})
			f.Close()
		}
		applog.Debug("image written", "image", img.Name, "disk", dk.Path, "verify", opts.Verify, "ssh", opts.Raspi.SSH, "user", opts.Raspi.User != nil, "error", err)
		switch {
		case ctx.Err() != nil:
			res.Cancelled = true
		case err != nil:
			res.Errors = []error{err}
			prog.OnItem(0, 1, img.Name, err)
		default:
			res.Done = 1
			prog.OnItem(1, 1, img.Name, nil)
		}
		return res
	})
	t.Label = img.Name + " → " + dk.Path
	t.onFinish = func(t *Task) {
		if t.ErrorCount == 0 && !t.Cancelled {
			t.CurrentName = dk.Path + ": done, eject it before unplugging"
			done := img.Name + " written onto " + dk.Path
			if opts.Raspi.SSH {
				done += ", SSH enabled"
			}
			if u := opts.Raspi.User; u != nil {
				done += ", user " + u.Name + " set"
			}
			m.setStatus("%s", done)
		}
	}
	m.dialog = Dialog{Kind: DialogProgress, Title: "Writing image", TaskID: t.ID}
}

// imageDiskStillThere reports whether dk, as listed in the write image
// dialog, is still a removable disk of the same name and size, other
// than the system's.
func (m *Model) imageDiskStillThere(dk imageDisk) bool {
	if drives.IsSystemDisk(dk.Path) || diskSizeOf(dk.Path) != dk.Size {
		return false
	}
	removable, _ := listRemovableDrives()
	for _, r := range removable {
		if drives.WholeDiskDevicePath(r.Path) == dk.Path {
			return strings.TrimSpace(driveDisplayName(r.Vendor, r.Model)) == dk.Name
		}
	}
	return false
}

// progressContext is a context cancelled once the task prog reports to is
// cancelled; stop releases it.
func progressContext(prog *fileops.Progress) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if prog.Cancelled != nil && prog.Cancelled() {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

// throttledBytes reports byte progress to prog a few times a second at
// most (always when now is set), as fileops' own operations do.
func throttledBytes(prog *fileops.Progress) func(b fileops.Bytes, now bool) {
	var mu sync.Mutex
	var last time.Time
	return func(b fileops.Bytes, now bool) {
		mu.Lock()
		defer mu.Unlock()
		if prog.OnBytes == nil || (!now && time.Since(last) < 250*time.Millisecond) {
			return
		}
		last = time.Now()
		prog.OnBytes(b)
	}
}

// writeImageDialogText is the write image dialog's body before the list
// of drives (see writeImageListTop).
func writeImageDialogText(d *Dialog) []string {
	img := d.Image
	lines := []string{
		styleAccent.Render(truncate(img.Name, 64)) + "  " + styleDim.Render(humanSize(img.Size)),
	}
	if img.NotHybrid {
		lines = append(lines, styleWarn.Render("Not a hybrid ISO: it may not boot from a USB drive"))
	}
	if img.RaspiOS {
		lines = append(lines, styleDim.Render("Raspberry Pi OS"))
	}
	lines = append(lines, "", "Write it onto:")
	return lines
}

// writeImageListTop is the row, within the write image dialog's content,
// of its first drive.
func writeImageListTop(d *Dialog) int {
	return len(writeImageDialogText(d))
}

func renderWriteImageDialog(b *strings.Builder, d *Dialog) string {
	for _, l := range writeImageDialogText(d) {
		b.WriteString(l + "\n")
	}
	if len(d.Items) == 0 {
		b.WriteString(styleDim.Render("  no removable drive: plug one in, then press r") + "\n")
	}
	for i, it := range d.Items {
		prefix, s := "  ", styleFile
		if d.ImageDisks[i].Unfit != "" {
			s = styleDim
		}
		if i == d.ItemIdx {
			prefix, s = "▸ ", styleAccent
		}
		b.WriteString(prefix + s.Render(truncate(it, 64)) + "\n")
	}
	check := "[ ]"
	if d.ImageVerify {
		check = "[x]"
	}
	b.WriteString("\n" + check + " Verify the drive after writing\n")
	help := "↑/↓ drive · v/Space verify · r refresh · Enter write · Esc cancel"
	if d.Image.RaspiOS {
		check = "[ ]"
		if d.ImageSSH {
			check = "[x]"
		}
		b.WriteString(check + " Enable SSH (an empty ssh file in its boot partition)\n")
		if d.ImageUser != nil {
			b.WriteString("[x] Create the user " + styleAccent.Render(d.ImageUser.Name) + " (userconf.txt)\n")
		} else {
			b.WriteString("[ ] Create a user (else asked for on first boot)\n")
		}
		help = "↑/↓ drive · v/Space verify · s SSH · u user · r refresh · Enter write · Esc cancel"
	}
	b.WriteString("\n" + styleDim.Render(help))
	return dialogBox(72).Render(b.String())
}

func renderWriteImageConfirm(b *strings.Builder, d *Dialog) string {
	dk := d.ImageDisks[d.ItemIdx]
	what := dk.Path
	if dk.Name != "" || dk.Size > 0 {
		var info []string
		if dk.Name != "" {
			info = append(info, dk.Name)
		}
		if dk.Size > 0 {
			info = append(info, humanSize(dk.Size))
		}
		what += " (" + strings.Join(info, ", ") + ")"
	}
	b.WriteString(styleErr.Render(fmt.Sprintf(
		"WARNING: ALL DATA on %s\nwill be LOST: overwritten by %s.\nThis cannot be undone.",
		what, truncate(d.Image.Name, 48))))
	b.WriteString("\n\n")
	if dk.Mounted {
		b.WriteString("Its filesystems will be unmounted first.\n")
	}
	if d.Image.RaspiOS && d.ImageSSH {
		b.WriteString("SSH will be enabled on it.\n")
	}
	if d.Image.RaspiOS && d.ImageUser != nil {
		b.WriteString("The user " + d.ImageUser.Name + " will be created on first boot.\n")
	}
	b.WriteString(styleDim.Render("Stopping the writing halfway leaves the drive unusable\nuntil it's formatted or written again.") + "\n")
	b.WriteString("\nContinue?\n\n")
	b.WriteString(styleDim.Render("y / Enter write · n / Esc cancel"))
	return dialogBox(64).Render(b.String())
}

func renderWriteImageUser(b *strings.Builder, d *Dialog) string {
	// The fields first: a click's row is the field's (see mouse.go).
	for i, label := range []string{"User name", "Password", "Repeat password"} {
		marker := "  "
		if i == d.FocusIdx {
			marker = "▸ "
		}
		b.WriteString(fmt.Sprintf("%s%-17s %s\n", marker, label+":", d.Inputs[i].View()))
	}
	if d.IsError && d.Message != "" {
		b.WriteString("\n" + styleErr.Render(d.Message) + "\n")
	}
	b.WriteString("\n" + styleDim.Render("Raspberry Pi OS creates it on first boot, in place\nof asking for one; the password is stored hashed.") + "\n")
	b.WriteString("\n" + styleDim.Render("Tab/↑/↓ field · Enter OK · Esc back"))
	return dialogBox(64).Render(b.String())
}
