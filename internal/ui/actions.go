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
	"fmt"
	"os"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"shfm/internal/config"
	"shfm/internal/drives"
	"shfm/internal/fileops"
	"shfm/internal/mtp"
	"shfm/internal/opener"
	"shfm/internal/secret"
	"shfm/internal/trash"
	"shfm/internal/vfs"
)

// --- copy / paste / move -------------------------------------------------------

// doCopyToClipboard puts the current selection (Ctrl+C) into the internal
// clipboard. Whether a later paste copies (Ctrl+V) or moves (Ctrl+Alt+V) is
// decided only at paste time.
func (m *Model) doCopyToClipboard() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	names := p.SelectedNames()
	if len(names) == 0 {
		m.setStatus("Nothing to copy")
		return
	}
	m.clipboard = Clipboard{FS: p.FS, Dir: p.Path, Names: names}
	m.publishClipboard()
	m.setStatus("%d item(s) ready to paste (Ctrl+V copy, Ctrl+Alt+V move)", len(names))
}

// doPaste pastes the clipboard's items into the active pane's current
// folder: as a copy if copyMode is true (Ctrl+V), moving them if false
// (Ctrl+Alt+V). The clipboard stays available after a copy (so it can be
// pasted again), but is cleared after a move.
func (m *Model) doPaste(copyMode bool) {
	if m.useExtClip {
		m.pasteExternal(copyMode)
		return
	}
	p := m.activePane()
	if p.Mode != PaneNormal || m.clipboard.Empty() {
		return
	}
	m.startTransfer(m.clipboard.FS, m.clipboard.Dir, m.clipboard.Names, p.FS, p.Path, copyMode)
	if !copyMode {
		m.clipboard = Clipboard{}
	}
}

// startTransfer runs a copy or move as a background Task and opens its
// progress dialog in the foreground; closing the dialog (Esc) just stops
// watching it — the task itself keeps running.
func (m *Model) startTransfer(srcFS vfs.FileSystem, srcDir string, names []string, destFS vfs.FileSystem, destDir string, copyMode bool) {
	items := make([]fileops.Item, len(names))
	for i, n := range names {
		items[i] = fileops.Item{FS: srcFS, Path: srcFS.Join(srcDir, n)}
	}
	kind := TaskMove
	if copyMode {
		kind = TaskCopy
	}
	t := m.startTask(kind, len(items), func(prog *fileops.Progress) *fileops.Result {
		if copyMode {
			return fileops.Copy(items, destFS, destDir, prog)
		}
		return fileops.Move(items, destFS, destDir, prog)
	})
	m.dialog = Dialog{Kind: DialogProgress, Title: t.Kind.String(), TaskID: t.ID}
}

// --- delete ----------------------------------------------------------------------

// deleteTargetLabel describes the item(s) about to be deleted for use in a
// confirmation message: the bare name when there's just one, otherwise a
// count.
func deleteTargetLabel(names []string) string {
	if len(names) == 1 {
		return fmt.Sprintf("%q", names[0])
	}
	return fmt.Sprintf("%d items", len(names))
}

func (m *Model) askDelete(useTrash bool) {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	names := p.SelectedNames()
	if len(names) == 0 {
		return
	}
	what := deleteTargetLabel(names)
	if useTrash {
		m.dialog = Dialog{Kind: DialogConfirmTrash, Title: "Move to trash",
			Message: fmt.Sprintf("Move %s to the trash?", what)}
	} else {
		m.dialog = Dialog{Kind: DialogConfirmPermanent, Title: "Delete permanently",
			Message: fmt.Sprintf("PERMANENTLY delete %s? This cannot be undone.", what)}
	}
}

func (m *Model) performDelete(useTrash bool) {
	p := m.activePane()
	names := p.SelectedNames()
	items := make([]fileops.Item, len(names))
	for i, n := range names {
		items[i] = fileops.Item{FS: p.FS, Path: p.FS.Join(p.Path, n)}
	}
	p.DeselectAll()
	t := m.startTask(TaskDelete, len(items), func(prog *fileops.Progress) *fileops.Result {
		return fileops.Delete(items, useTrash, prog)
	})
	m.dialog = Dialog{Kind: DialogProgress, Title: t.Kind.String(), TaskID: t.ID}
}

// --- rename / new file / new folder ------------------------------------------

func (m *Model) askRename() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	e, ok := p.CurrentEntry()
	if !ok || IsParentEntry(e) {
		return
	}
	m.dialog = newSingleInputDialog(DialogRename, "Rename", "new name", e.Name)
}

func (m *Model) askNewFile() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	m.dialog = newSingleInputDialog(DialogNewFile, "New file", "file name", "")
}

func (m *Model) askNewFolder() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	m.dialog = newSingleInputDialog(DialogNewFolder, "New folder", "folder name", "")
}

// openNewItemChoice is triggered by the "[+]" button next to the PATH
// field: lets the user pick between a new file and a new folder.
func (m *Model) openNewItemChoice() {
	m.dialog = Dialog{Kind: DialogNewChoice, Title: "New...", Items: []string{"New file", "New folder"}}
}

// --- trash (Freedesktop.org Trash Specification) ---------------------------------

func (m *Model) toggleTrashView() {
	p := m.activePane()
	if p.Mode == PaneNormal {
		p.Mode = PaneTrash
	} else {
		p.Mode = PaneNormal
	}
	p.Cursor, p.Offset = 0, 0
	p.Load()
}

func (m *Model) restoreTrashCurrent() {
	p := m.activePane()
	it, ok := p.CurrentTrashItem()
	if !ok {
		return
	}
	if err := trash.Restore(it); err != nil {
		m.setError("Restore failed: %v", err)
	} else {
		m.setStatus("Restored to %s", it.OriginalPath)
	}
	p.Load()
}

func (m *Model) askEmptyTrash() {
	m.dialog = Dialog{Kind: DialogConfirmEmptyTrash, Title: "Empty trash",
		Message: "Permanently empty the trash? This cannot be undone."}
}

// --- help ------------------------------------------------------------------------

func (m *Model) openHelp() {
	m.dialog = Dialog{Kind: DialogHelp, Title: "Help"}
}

// --- properties (permissions, owner, group) ---------------------------------------

func (m *Model) openProperties() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	e, ok := p.CurrentEntry()
	if !ok || IsParentEntry(e) {
		return
	}
	fullPath := p.FS.Join(p.Path, e.Name)
	d := Dialog{Kind: DialogProperties, Title: "Properties", PropsFS: p.FS, PropsPath: fullPath, PropsEntry: e}
	if ar, ok := p.FS.(vfs.AttrReader); ok {
		d.PropsAttrs = ar.Attributes(fullPath)
	}
	if _, ok := p.FS.(vfs.PermissionsEditor); ok {
		modeInput := textinput.New()
		modeInput.SetValue(fmt.Sprintf("%o", e.Mode.Perm()))
		modeInput.CharLimit = 4
		modeInput.SetWidth(10)
		modeInput.Focus()
		ownerInput := textinput.New()
		ownerInput.SetValue(e.Owner)
		ownerInput.SetWidth(20)
		groupInput := textinput.New()
		groupInput.SetValue(e.Group)
		groupInput.SetWidth(20)
		d.Inputs = []textinput.Model{modeInput, ownerInput, groupInput}
		d.FocusIdx = 0
	}
	m.dialog = d
}

// applyProperties runs chmod/chown off the event loop (see
// runElevatable): either may need root, and pkexec's prompt with it. Only
// what was actually changed is applied, and a backend that can do both at
// once (vfs.OwnerModeSetter) asks for the password once.
func (m *Model) applyProperties() tea.Cmd {
	d := m.dialog
	m.dialog = Dialog{}
	pe, ok := d.PropsFS.(vfs.PermissionsEditor)
	if !ok || len(d.Inputs) < 3 {
		return nil
	}
	modeStr := strings.TrimSpace(d.Inputs[0].Value())
	ownerStr := strings.TrimSpace(d.Inputs[1].Value())
	groupStr := strings.TrimSpace(d.Inputs[2].Value())

	modeVal, err := strconv.ParseUint(modeStr, 8, 32)
	if err != nil {
		m.setError("Invalid mode (expected octal, e.g. 644)")
		return nil
	}
	mode := os.FileMode(modeVal)
	modeChanged := mode.Perm() != d.PropsEntry.Mode.Perm()
	ownerChanged := ownerStr != d.PropsEntry.Owner || groupStr != d.PropsEntry.Group
	uid, gid := -1, -1
	if ownerChanged {
		var uerr, gerr error
		uid, uerr = vfs.ResolveUser(ownerStr)
		gid, gerr = vfs.ResolveGroup(groupStr)
		if uerr != nil || gerr != nil {
			m.setError("Unknown owner or group")
			return nil
		}
	}
	if !modeChanged && !ownerChanged {
		m.setStatus("Nothing changed")
		return nil
	}
	path := d.PropsPath

	return runElevatable(func() elevatedDoneMsg {
		var err error
		switch oms, both := pe.(vfs.OwnerModeSetter); {
		case modeChanged && ownerChanged && both:
			err = oms.SetOwnerAndMode(path, uid, gid, mode)
		case ownerChanged:
			err = pe.Chown(path, uid, gid)
			if err == nil && modeChanged {
				err = pe.Chmod(path, mode)
			}
		default:
			err = pe.Chmod(path, mode)
		}
		if err != nil {
			return elevatedDoneMsg{err: fmt.Sprintf("Properties not applied: %v", err)}
		}
		return elevatedDoneMsg{status: "Properties updated"}
	})
}

// --- open with default application ------------------------------------------------

func (m *Model) openWithDefaultApp(e vfs.Entry) {
	p := m.activePane()
	fullVfsPath := p.FS.Join(p.Path, e.Name)
	mimeType := opener.MimeType(e.Name)

	// The local backend has a real path an external app can open directly;
	// every other source (SMB/NFS/SFTP/MTP) needs its content downloaded to
	// a local temp copy first (see openremote.go) \u2014 set up here and used by
	// both branches below.
	var realPath string
	var remote *remoteOpenTarget
	if lp, ok := p.FS.(vfs.LocalPath); ok {
		rp, ok := lp.LocalPath(fullVfsPath)
		if !ok {
			m.setStatus("%s (%s)", e.Name, humanSize(e.Size))
			return
		}
		realPath = rp
	} else {
		remote = &remoteOpenTarget{fs: p.FS, path: fullVfsPath, name: e.Name}
	}

	if app, ok := opener.DefaultApp(mimeType); ok {
		if remote != nil {
			m.startOpenRemote(remote, app)
			return
		}
		if err := opener.Launch(app, realPath); err != nil {
			m.setError("Could not open %s: %v", e.Name, err)
			return
		}
		m.setStatus("Opened %s with %s", e.Name, app.Name)
		return
	}

	apps := opener.ListApps()
	if len(apps) == 0 {
		m.setStatus("%s (%s) \u2014 no application found to open it with", e.Name, humanSize(e.Size))
		return
	}
	items := make([]string, len(apps))
	for i, a := range apps {
		items[i] = a.Name
	}
	m.dialog = Dialog{
		Kind: DialogChooseApp, Title: "Open " + e.Name + " with\u2026",
		Items: items, ChooseApps: apps, ChooseAppMime: mimeType,
		ChooseAppTarget: realPath, ChooseAppRemote: remote,
	}
}

// --- background tasks --------------------------------------------------------------

func (m *Model) openTaskList() {
	items := make([]string, len(m.tasks))
	for i, t := range m.tasks {
		items[i] = t.Summary()
	}
	m.dialog = Dialog{Kind: DialogTaskList, Title: "Background tasks", Items: items}
}

// --- sources: local, removable, MTP, SMB, NFS, SFTP --------------------------------

type sourceMenuEntry struct {
	label     string
	kind      string // "local" | "removable-mounted" | "removable-unmounted" | "format-request" | "mtp" | "remote" | "new-smb" | "new-nfs" | "new-sftp"
	local     drives.LocalDrive
	remote    config.RemoteSource
	removable drives.RemovableDevice
	mtpDevice mtp.DeviceInfo
}

// sourceMenuGroup buckets a sourceMenuEntry.kind into a coarser category,
// so the source picker can show a blank separator line between categories
// (local disks / removable devices / MTP / remote sources / "new
// connection" actions) instead of one dense, undifferentiated list.
func sourceMenuGroup(kind string) int {
	switch kind {
	case "local":
		return 0
	case "removable-mounted", "removable-unmounted", "format-request":
		return 1
	case "mtp":
		return 2
	case "remote":
		return 3
	default: // "new-smb", "new-nfs", "new-sftp"
		return 4
	}
}

// sourceMenuRows maps each entry's index to the row it's rendered on once a
// blank separator line is inserted between groups (see sourceMenuGroup) —
// shared by renderDialogBox and handleDialogMouse so the two always agree
// on the layout; a mouse click on a row not present in this mapping (i.e.
// on a separator) simply hits nothing.
func sourceMenuRows(entries []sourceMenuEntry) []int {
	rows := make([]int, len(entries))
	row := 0
	for i, e := range entries {
		if i > 0 && sourceMenuGroup(e.kind) != sourceMenuGroup(entries[i-1].kind) {
			row++
		}
		rows[i] = row
		row++
	}
	return rows
}

// driveDisplayName combines a disk's vendor/model (see drives.diskInfo)
// into a human-recognizable name for the source picker, e.g. "Samsung SSD
// 860 EVO 1TB " — with a trailing space when non-empty, so callers can
// splice it directly in front of a parenthesized "(fstype, size)" without
// a conditional space of their own; empty when neither is known (e.g. a
// virtual disk, a non-Linux platform, or a permissions issue reading
// sysfs), in which case the label simply omits it.
func driveDisplayName(vendor, model string) string {
	name := strings.TrimSpace(vendor + " " + model)
	if name == "" {
		return ""
	}
	return name + " "
}

func (m *Model) openSourceMenu() {
	local, _ := drives.ListLocal()
	removable, _ := drives.ListRemovable()
	mtpDevices, _ := mtp.DiscoverDevices()

	// Removable devices already mounted are shown in their own section
	// (with the [USB] icon) rather than among the generic disks, to avoid
	// listing the same mount point twice.
	removableMounted := map[string]bool{}
	for _, r := range removable {
		if r.Mounted {
			removableMounted[r.Path] = true
		}
	}

	var entries []sourceMenuEntry
	for _, d := range local {
		if removableMounted[d.Device] {
			continue
		}
		name := driveDisplayName(d.Vendor, d.Model)
		label := fmt.Sprintf("%s %s  %s(%s, %s, %s free)", iconSourceLocal, d.MountPoint, name, d.FSType, humanSize(int64(d.Total)), humanSize(int64(d.Free)))
		entries = append(entries, sourceMenuEntry{label: label, kind: "local", local: d})
	}
	for _, r := range removable {
		name := driveDisplayName(r.Vendor, r.Model)
		if r.Mounted {
			label := fmt.Sprintf("%s %s \u2192 %s  %s(%s, %s)", iconSourceRemovable, r.Path, r.MountPoint, name, r.FSType, humanSize(int64(r.SizeBytes)))
			entries = append(entries, sourceMenuEntry{label: label, kind: "removable-mounted", removable: r})
		} else {
			label := fmt.Sprintf("%s %s  %s(not mounted, %s) \u2014 mount and open", iconSourceRemovable, r.Path, name, humanSize(int64(r.SizeBytes)))
			entries = append(entries, sourceMenuEntry{label: label, kind: "removable-unmounted", removable: r})
		}
		if !drives.IsSystemDisk(r.Path) {
			label := fmt.Sprintf("%s Format %s\u2026", iconSourceFormat, r.Path)
			entries = append(entries, sourceMenuEntry{label: label, kind: "format-request", removable: r})
		}
	}
	for _, dev := range mtpDevices {
		label := fmt.Sprintf("%s %s", iconSourceMTP, dev.Label())
		entries = append(entries, sourceMenuEntry{label: label, kind: "mtp", mtpDevice: dev})
	}
	for _, r := range m.cfg.RemoteSources {
		var icon, target string
		switch r.Kind {
		case "nfs":
			icon, target = iconSourceNFS, r.Export
		case "sftp":
			icon, target = iconSourceSFTP, r.RemotePath
		default:
			icon, target = iconSourceSMB, r.Share
		}
		label := fmt.Sprintf("%s %s:%s  [%s]", icon, r.Host, target, r.Name)
		entries = append(entries, sourceMenuEntry{label: label, kind: "remote", remote: r})
	}
	entries = append(entries, sourceMenuEntry{label: iconSourceAdd + " New SMB connection\u2026", kind: "new-smb"})
	entries = append(entries, sourceMenuEntry{label: iconSourceAdd + " New NFS mount\u2026", kind: "new-nfs"})
	entries = append(entries, sourceMenuEntry{label: iconSourceAdd + " New SFTP connection\u2026", kind: "new-sftp"})

	m.sourceMenuEntries = entries
	items := make([]string, len(entries))
	for i, e := range entries {
		items[i] = e.label
	}
	m.dialog = Dialog{Kind: DialogSourceMenu, Title: "Source for the " + paneSide(m.active) + " pane", Items: items}
}

func paneSide(idx int) string {
	if idx == 0 {
		return "left"
	}
	return "right"
}

func (m *Model) selectSourceMenuItem() {
	idx := m.dialog.ItemIdx
	if idx < 0 || idx >= len(m.sourceMenuEntries) {
		m.dialog = Dialog{}
		return
	}
	entry := m.sourceMenuEntries[idx]
	switch entry.kind {
	case "local":
		fs := vfs.NewLocalFS(entry.local.MountPoint, entry.local.MountPoint)
		m.replaceActiveFS(fs, fs.Root())
		m.dialog = Dialog{}
		m.setStatus("Opened %s", entry.local.MountPoint)
	case "removable-mounted":
		fs := vfs.NewLocalFS(entry.removable.MountPoint, entry.removable.MountPoint)
		m.replaceActiveFS(fs, fs.Root())
		m.dialog = Dialog{}
		m.setStatus("Opened %s (%s)", entry.removable.MountPoint, entry.removable.Path)
	case "removable-unmounted":
		m.dialog = Dialog{}
		mp, err := drives.TryAutoMount(entry.removable.Path, entry.removable.Name)
		if err != nil {
			m.setError("Could not mount %s: %v", entry.removable.Path, err)
			return
		}
		fs := vfs.NewLocalFS(mp, mp)
		m.replaceActiveFS(fs, fs.Root())
		m.setStatus("Mounted %s at %s", entry.removable.Path, mp)
	case "format-request":
		m.openFormatChoose(entry.removable)
	case "mtp":
		mtpDevice := entry.mtpDevice
		m.startConnect(m.active, mtpDevice.Label(), func() (vfs.FileSystem, error) {
			return vfs.DialMTP(mtpDevice)
		}, nil, nil)
	case "remote":
		m.connectSavedRemote(entry.remote)
	case "new-smb":
		m.dialog = newConnectDialog(DialogConnectSMB)
	case "new-nfs":
		m.dialog = newConnectDialog(DialogConnectNFS)
	case "new-sftp":
		m.dialog = newConnectDialog(DialogConnectSFTP)
	}
}

// connectSavedRemote pre-fills the connection form with the saved data
// (host, share/export, user...). If a password was saved (encrypted at
// rest) for this source, it's decrypted and pre-filled automatically so it
// doesn't need to be retyped every time; otherwise it must be entered again.
func (m *Model) connectSavedRemote(r config.RemoteSource) {
	switch r.Kind {
	case "smb":
		d := newConnectDialog(DialogConnectSMB)
		d.Inputs[0].SetValue(r.Host)
		d.Inputs[1].SetValue(r.Share)
		d.Inputs[2].SetValue(r.Domain)
		d.Inputs[3].SetValue(r.User)
		if pass, err := r.DecryptedPassword(); err == nil && pass != "" {
			d.Inputs[4].SetValue(pass)
		} else if err != nil {
			m.setError("Could not decrypt the saved password for %s: %v", r.Name, err)
		}
		focusConnectField(&d, 4)
		m.dialog = d
	case "sftp":
		d := newConnectDialog(DialogConnectSFTP)
		d.Inputs[0].SetValue(r.Host)
		if r.Port != 0 {
			d.Inputs[1].SetValue(strconv.Itoa(r.Port))
		}
		d.Inputs[2].SetValue(r.User)
		if pass, err := r.DecryptedPassword(); err == nil && pass != "" {
			d.Inputs[3].SetValue(pass)
		}
		d.Inputs[4].SetValue(r.RemotePath)
		focusConnectField(&d, 3)
		m.dialog = d
	default: // "nfs"
		d := newConnectDialog(DialogConnectNFS)
		d.Inputs[0].SetValue(r.Host)
		d.Inputs[1].SetValue(r.Export)
		m.dialog = d
	}
}

func focusConnectField(d *Dialog, idx int) {
	for i := range d.Inputs {
		d.Inputs[i].Blur()
	}
	d.FocusIdx = idx
	d.Inputs[idx].Focus()
}

// replaceFS swaps pane idx's source for fs, opened at path. Used both by
// the (fast, synchronous) local/removable-disk selection and, via
// connect.go's handleConnectResult, by asynchronous network/USB
// connections — always addressing the pane by its original index rather
// than "whichever pane is active now", since by the time a slow
// connection attempt completes the user may well have switched panes.
func (m *Model) replaceFS(idx int, fs vfs.FileSystem, path string) {
	old := m.panes[idx].FS
	m.panes[idx] = NewPane(fs, path, m.cfg.ShowHidden, idx, m.sizeCh)
	if old != nil && old != fs {
		m.closeFSWhenUnused(old)
	}
	// A newly opened source may complete a mirror pair.
	m.checkMirrors()
}

func (m *Model) replaceActiveFS(fs vfs.FileSystem, path string) {
	m.replaceFS(m.active, fs, path)
}

func (m *Model) doConnectSMB() {
	d := m.dialog
	host := strings.TrimSpace(d.Inputs[0].Value())
	share := strings.TrimSpace(d.Inputs[1].Value())
	domain := strings.TrimSpace(d.Inputs[2].Value())
	user := strings.TrimSpace(d.Inputs[3].Value())
	pass := d.Inputs[4].Value()
	if host == "" || share == "" {
		m.dialog.IsError = true
		m.dialog.Message = "Host and share are required"
		return
	}
	label := "smb://" + host + "/" + share
	paneIdx := m.active
	m.startConnect(paneIdx, label, func() (vfs.FileSystem, error) {
		return vfs.DialSMB(vfs.SMBOptions{
			Host: host, Share: share, Domain: domain, User: user, Password: pass, Guest: user == "",
		})
	}, func() {
		src := config.RemoteSource{
			Name: host + "/" + share, Kind: "smb", Host: host, Share: share,
			Domain: domain, User: user, Guest: user == "",
		}
		if pass != "" {
			if enc, err := secret.Encrypt(pass); err == nil {
				src.EncryptedPassword = enc
			}
		}
		m.saveRemoteSource(src)
	}, func(errText string) {
		reopened := d
		reopened.IsError = true
		reopened.Message = errText
		m.dialog = reopened
	})
}

func (m *Model) doConnectNFS() {
	d := m.dialog
	host := strings.TrimSpace(d.Inputs[0].Value())
	export := strings.TrimSpace(d.Inputs[1].Value())
	if host == "" || export == "" {
		m.dialog.IsError = true
		m.dialog.Message = "Host and export are required"
		return
	}
	// An empty UID/GID field must NOT silently become 0 (root): most NFS
	// servers apply root_squash, which rejects root credentials outright,
	// so falling back to root here would just trade one confusing error for
	// another. The dialog pre-fills the local user's own UID/GID already;
	// this only matters if the field was cleared by hand.
	uid, uerr := strconv.ParseUint(strings.TrimSpace(d.Inputs[2].Value()), 10, 32)
	if uerr != nil {
		uid = uint64(os.Getuid())
	}
	gid, gerr := strconv.ParseUint(strings.TrimSpace(d.Inputs[3].Value()), 10, 32)
	if gerr != nil {
		gid = uint64(os.Getgid())
	}
	label := "nfs://" + host + export
	paneIdx := m.active
	m.startConnect(paneIdx, label, func() (vfs.FileSystem, error) {
		return vfs.DialNFS(vfs.NFSOptions{Host: host, Export: export, UID: uint32(uid), GID: uint32(gid)})
	}, func() {
		m.saveRemoteSource(config.RemoteSource{Name: host + export, Kind: "nfs", Host: host, Export: export})
	}, func(errText string) {
		reopened := d
		reopened.IsError = true
		reopened.Message = errText
		m.dialog = reopened
	})
}

func (m *Model) doConnectSFTP() {
	d := m.dialog
	host := strings.TrimSpace(d.Inputs[0].Value())
	portStr := strings.TrimSpace(d.Inputs[1].Value())
	user := strings.TrimSpace(d.Inputs[2].Value())
	pass := d.Inputs[3].Value()
	remotePath := strings.TrimSpace(d.Inputs[4].Value())
	if host == "" || user == "" {
		m.dialog.IsError = true
		m.dialog.Message = "Host and user are required"
		return
	}
	port := 22
	if portStr != "" {
		if v, err := strconv.Atoi(portStr); err == nil {
			port = v
		}
	}
	label := "sftp://" + user + "@" + host
	paneIdx := m.active
	m.startConnect(paneIdx, label, func() (vfs.FileSystem, error) {
		return vfs.DialSFTP(vfs.SFTPOptions{Host: host, Port: port, User: user, Password: pass, BasePath: remotePath})
	}, func() {
		src := config.RemoteSource{Name: user + "@" + host, Kind: "sftp", Host: host, Port: port, User: user, RemotePath: remotePath}
		if pass != "" {
			if enc, err := secret.Encrypt(pass); err == nil {
				src.EncryptedPassword = enc
			}
		}
		m.saveRemoteSource(src)
	}, func(errText string) {
		reopened := d
		reopened.IsError = true
		reopened.Message = errText
		m.dialog = reopened
	})
}

func (m *Model) saveRemoteSource(r config.RemoteSource) {
	for _, existing := range m.cfg.RemoteSources {
		if existing.Kind == r.Kind && existing.Host == r.Host && existing.Share == r.Share &&
			existing.Export == r.Export && existing.RemotePath == r.RemotePath {
			return
		}
	}
	m.cfg.RemoteSources = append(m.cfg.RemoteSources, r)
	m.cfg.Save()
}

// --- format a removable source ------------------------------------------------------

func (m *Model) openFormatChoose(dev drives.RemovableDevice) {
	items := make([]string, len(drives.FormatChoices))
	for i, c := range drives.FormatChoices {
		items[i] = c.Label
	}
	m.dialog = Dialog{Kind: DialogFormatChoose, Title: "Format " + dev.Path, Items: items, FormatDevice: dev}
}

// --- confirm dialog handling ---------------------------------------------------------

func (m *Model) confirmDialog() (tea.Cmd, bool) {
	d := m.dialog
	switch d.Kind {
	case DialogRename:
		newName := strings.TrimSpace(d.Inputs[0].Value())
		if newName != "" {
			p := m.activePane()
			if e, ok := p.CurrentEntry(); ok {
				m.dialog = Dialog{}
				fs, path := p.FS, p.FS.Join(p.Path, e.Name)
				return runElevatable(func() elevatedDoneMsg {
					if err := fileops.Rename(fs, path, newName); err != nil {
						return elevatedDoneMsg{err: fmt.Sprintf("Rename failed: %v", err)}
					}
					return elevatedDoneMsg{status: "Renamed to " + newName}
				}), true
			}
		}
		m.dialog = Dialog{}

	case DialogNewFile:
		name := strings.TrimSpace(d.Inputs[0].Value())
		if name != "" {
			p := m.activePane()
			if err := p.FS.CreateEmptyFile(p.FS.Join(p.Path, name)); err != nil {
				m.setError("Could not create file: %v", err)
			} else {
				m.setStatus("Created %s", name)
			}
			p.Load()
		}
		m.dialog = Dialog{}

	case DialogNewFolder:
		name := strings.TrimSpace(d.Inputs[0].Value())
		if name != "" {
			p := m.activePane()
			if err := p.FS.Mkdir(p.FS.Join(p.Path, name)); err != nil {
				m.setError("Could not create folder: %v", err)
			} else {
				m.setStatus("Created folder %s", name)
			}
			p.Load()
		}
		m.dialog = Dialog{}

	case DialogNewChoice:
		if d.ItemIdx == 0 {
			m.askNewFile()
		} else {
			m.askNewFolder()
		}

	case DialogConfirmTrash:
		m.performDelete(true)

	case DialogConfirmPermanent:
		m.performDelete(false)

	case DialogConfirmEmptyTrash:
		if err := trash.Empty(); err != nil {
			m.setError("Could not empty the trash: %v", err)
		} else {
			m.setStatus("Trash emptied")
		}
		m.activePane().Load()
		m.dialog = Dialog{}

	case DialogConfirmQuit:
		m.quitting = true
		m.dialog = Dialog{}

	case DialogConnectSMB:
		m.doConnectSMB()

	case DialogConnectNFS:
		m.doConnectNFS()

	case DialogConnectSFTP:
		m.doConnectSFTP()

	case DialogSourceMenu:
		m.selectSourceMenuItem()

	case DialogChooseApp:
		if d.ItemIdx < 0 || d.ItemIdx >= len(d.ChooseApps) {
			m.dialog = Dialog{}
			break
		}
		app := d.ChooseApps[d.ItemIdx]
		m.dialog = Dialog{}
		_ = opener.SaveDefaultApp(d.ChooseAppMime, app)
		if d.ChooseAppRemote != nil {
			m.startOpenRemote(d.ChooseAppRemote, app)
			break
		}
		if err := opener.Launch(app, d.ChooseAppTarget); err != nil {
			m.setError("Could not launch %s: %v", app.Name, err)
			break
		}
		m.setStatus("Opened with %s (remembered as default for %s)", app.Name, d.ChooseAppMime)

	case DialogProgress:
		m.dialog = Dialog{}

	case DialogTaskList:
		if d.ItemIdx >= 0 && d.ItemIdx < len(m.tasks) {
			t := m.tasks[d.ItemIdx]
			m.dialog = Dialog{Kind: DialogProgress, Title: t.Kind.String(), TaskID: t.ID}
		} else {
			m.dialog = Dialog{}
		}

	case DialogProperties:
		return m.applyProperties(), true

	case DialogFormatChoose:
		if d.ItemIdx < 0 || d.ItemIdx >= len(drives.FormatChoices) {
			m.dialog = Dialog{}
			break
		}
		choice := drives.FormatChoices[d.ItemIdx]
		m.dialog = Dialog{
			Kind: DialogFormatConfirm1, Title: "Confirm format",
			FormatDevice: d.FormatDevice, FormatFSType: choice.Type,
		}

	case DialogFormatConfirm1:
		ti := textinput.New()
		ti.Placeholder = "YES"
		ti.SetWidth(10)
		ti.Focus()
		m.dialog = Dialog{
			Kind: DialogFormatConfirm2, Title: "Final confirmation",
			FormatDevice: d.FormatDevice, FormatFSType: d.FormatFSType,
			Inputs: []textinput.Model{ti},
		}

	case DialogFormatConfirm2:
		if strings.TrimSpace(d.Inputs[0].Value()) != "YES" {
			// Wrong/incomplete confirmation text: stay on this dialog so
			// the user can correct it, rather than silently discarding
			// an irreversible, destructive action's confirmation step.
			return nil, false
		}
		wholeDisk := drives.WholeDiskDevicePath(d.FormatDevice.Path)
		fsType := d.FormatFSType
		label := fmt.Sprintf("%s as %s", wholeDisk, fsType)
		m.dialog = Dialog{}
		t := m.startSimpleTask(TaskFormat, label, func() error {
			return drives.FormatDevice(wholeDisk, fsType)
		})
		m.dialog = Dialog{Kind: DialogProgress, Title: "Formatting", TaskID: t.ID}

	case DialogMirrorConfirm:
		m.confirmMirror()

	case DialogMirrorConfirmDelete:
		m.deleteMirror(d.MirrorPairID)

	case DialogHelp, DialogMessage, DialogConnecting:
		m.dialog = Dialog{}
	}
	if m.quitting {
		// DialogConfirmQuit's case above (or any future path that decides
		// to quit) only sets the flag; issuing the actual tea.Quit command
		// happens here, once, regardless of which case set it — the flag
		// alone does nothing on its own (View() would just render an
		// empty screen forever without the program ever actually exiting,
		// which is exactly the "the whole thing freezes" bug this fixes).
		return tea.Quit, true
	}
	return nil, true
}
