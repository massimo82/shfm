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
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/applog"
	"shfm/internal/config"
	"shfm/internal/fileops"
	"shfm/internal/mtp"
	"shfm/internal/vfs"
	"shfm/internal/wlclip"
)

// Shared system clipboard (Wayland, see internal/wlclip), enabled by the
// "share_clipboard" config option (default on). Both ways:
//
//   - Ctrl+C in shfm also puts the copied items on the system clipboard,
//     as file URIs (see fileuri.go) other applications can paste;
//   - files copied in another application (text/uri-list or GNOME's
//     x-special/gnome-copied-files) become what Ctrl+V / Ctrl+Alt+V
//     paste in shfm, until something is copied in shfm again. Whether it
//     copies or moves is always decided by the shfm key used.
//
// URIs of network/MTP sources are resolved against a pane that has the
// source open, else a saved source (connected in the background just for
// the transfer), else a connected MTP device.

type sysclipReadyMsg struct {
	c   *wlclip.Client
	err error
}

// sysclipFilesMsg reports files another application put on the clipboard.
type sysclipFilesMsg struct{ uris []string }

var clipboardFileTypes = []string{"text/uri-list", "x-special/gnome-copied-files"}

func connectSysclip() tea.Msg {
	c, err := wlclip.Connect()
	return sysclipReadyMsg{c: c, err: err}
}

func (m *Model) waitForSysclipMsg() tea.Cmd {
	return func() tea.Msg { return <-m.sysclipCh }
}

func (m *Model) handleSysclipReady(msg sysclipReadyMsg) {
	if msg.err != nil {
		applog.Info("system clipboard not shared", "error", msg.err)
		return
	}
	m.sysclip = msg.c
	ch := m.sysclipCh
	c := msg.c
	fetch := func() {
		mimes, owned := c.Selection()
		if owned {
			return
		}
		for _, t := range clipboardFileTypes {
			if !slices.Contains(mimes, t) {
				continue
			}
			data, err := c.Receive(t, 2*time.Second)
			if err != nil {
				applog.Debug("reading the system clipboard failed", "type", t, "error", err)
				continue
			}
			if uris := parseURIList(data); len(uris) > 0 {
				ch <- sysclipFilesMsg{uris: uris}
				return
			}
		}
	}
	c.OnChange(func() { go fetch() })
	go fetch() // files copied elsewhere before shfm started
}

func (m *Model) handleSysclipFiles(msg sysclipFilesMsg) {
	m.extClip = msg.uris
	m.useExtClip = true
	m.setStatus("%d item(s) from the system clipboard ready to paste", len(msg.uris))
}

// publishClipboard puts shfm's clipboard on the system clipboard.
func (m *Model) publishClipboard() {
	m.useExtClip = false
	if m.sysclip == nil || m.clipboard.Empty() {
		return
	}
	var uris, text []string
	for _, p := range m.clipboard.Paths() {
		u := fileURI(m.clipboard.FS, p)
		if u == "" {
			continue
		}
		uris = append(uris, u)
		if m.clipboard.FS.Kind() == vfs.KindLocal {
			text = append(text, p)
		} else {
			text = append(text, u)
		}
	}
	if len(uris) == 0 {
		return
	}
	plain := []byte(strings.Join(text, "\n"))
	err := m.sysclip.SetSelection(map[string][]byte{
		"text/uri-list":                []byte(strings.Join(uris, "\r\n") + "\r\n"),
		"x-special/gnome-copied-files": []byte("copy\n" + strings.Join(uris, "\n")),
		"text/plain;charset=utf-8":     plain,
		"text/plain":                   plain,
		"UTF8_STRING":                  plain,
	})
	if err != nil {
		applog.Warn("sharing the clipboard failed", "error", err)
		m.sysclip.Close()
		m.sysclip = nil
	}
}

// clipboardCount is how many items a paste would bring in.
func (m *Model) clipboardCount() int {
	if m.useExtClip {
		return len(m.extClip)
	}
	if m.clipboard.Empty() {
		return 0
	}
	return len(m.clipboard.Names)
}

// --- pasting files from the system clipboard -----------------------------------

// extSource is where a clipboard URI lives: an open source (fs), or one to
// connect to just for the transfer (dial).
type extSource struct {
	label string
	fs    vfs.FileSystem
	dial  func() (vfs.FileSystem, error)
}

// extGroup is a batch of clipboard items in the same folder of a source.
type extGroup struct {
	src   extSource
	dir   string
	names []string
}

// resolveURI finds the source of a clipboard URI.
func (m *Model) resolveURI(u string, local vfs.FileSystem) (extSource, string, error) {
	scheme, rest, ok := decodeURI(u)
	if !ok {
		return extSource{}, "", fmt.Errorf("not a URI: %s", u)
	}
	if scheme == "file" {
		// "file:///p" (rest "/p"), or "file://localhost/p".
		path := rest
		if !strings.HasPrefix(path, "/") {
			_, path, _ = strings.Cut(rest, "/")
			path = "/" + path
		}
		return extSource{label: "Local", fs: local}, path, nil
	}
	for _, p := range m.panes {
		if p.FS.Kind() == vfs.KindLocal {
			continue
		}
		if path, ok := pathUnder(p.FS.Label(), scheme, rest); ok {
			return extSource{label: p.FS.Label(), fs: p.FS}, path, nil
		}
	}
	for _, r := range m.cfg.RemoteSources {
		label, dial := savedSourceDialer(r)
		if path, ok := pathUnder(label, scheme, rest); ok {
			return extSource{label: label, dial: dial}, path, nil
		}
	}
	if scheme == "mtp" {
		devices, _ := mtp.DiscoverDevices()
		for _, d := range devices {
			label := "mtp://" + d.Label()
			if path, ok := pathUnder(label, scheme, rest); ok {
				dev := d
				return extSource{label: label, dial: func() (vfs.FileSystem, error) { return vfs.DialMTP(dev) }}, path, nil
			}
		}
	}
	return extSource{}, "", fmt.Errorf("no open or saved source for %s", u)
}

// savedSourceDialer returns a saved source's label (as its VFS backend
// would name it) and a function connecting to it with the saved details.
func savedSourceDialer(r config.RemoteSource) (string, func() (vfs.FileSystem, error)) {
	pass := func() string {
		p, _ := r.DecryptedPassword()
		return p
	}
	switch r.Kind {
	case "smb":
		return fmt.Sprintf("smb://%s/%s", r.Host, r.Share), func() (vfs.FileSystem, error) {
			return vfs.DialSMB(vfs.SMBOptions{Host: r.Host, Port: r.Port, Share: r.Share, Domain: r.Domain,
				User: r.User, Password: pass(), Guest: r.Guest})
		}
	case "sftp":
		return fmt.Sprintf("sftp://%s@%s", r.User, r.Host), func() (vfs.FileSystem, error) {
			return vfs.DialSFTP(vfs.SFTPOptions{Host: r.Host, Port: r.Port, User: r.User, Password: pass(), BasePath: r.RemotePath})
		}
	default: // "nfs"
		return fmt.Sprintf("nfs://%s%s", r.Host, r.Export), func() (vfs.FileSystem, error) {
			return vfs.DialNFS(vfs.NFSOptions{Host: r.Host, Export: r.Export, UID: uint32(os.Getuid()), GID: uint32(os.Getgid())})
		}
	}
}

// extGroups resolves the system clipboard's URIs into per-folder batches.
func (m *Model) extGroups() ([]extGroup, error) {
	local := vfs.NewLocalFS("Local", "/")
	var groups []extGroup
	for _, u := range m.extClip {
		src, path, err := m.resolveURI(u, local)
		if err != nil {
			return nil, err
		}
		fs := src.fs
		if fs == nil {
			fs = local // only for path manipulation: all backends use "/" paths
		}
		dir, name := fs.Dir(path), fs.Base(path)
		idx := slices.IndexFunc(groups, func(g extGroup) bool { return g.src.label == src.label && g.dir == dir })
		if idx < 0 {
			groups = append(groups, extGroup{src: src, dir: dir})
			idx = len(groups) - 1
		}
		groups[idx].names = append(groups[idx].names, name)
	}
	return groups, nil
}

// pasteExternal pastes the system clipboard's files into the active pane.
func (m *Model) pasteExternal(copyMode bool) {
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	groups, err := m.extGroups()
	if err != nil {
		m.setError("Can't paste: %v", err)
		return
	}
	for _, g := range groups {
		if g.src.fs != nil {
			m.startTransfer(g.src.fs, g.dir, g.names, p.FS, p.Path, copyMode)
			continue
		}
		m.startDialTransfer(g, p.FS, p.Path, copyMode)
	}
	if !copyMode {
		m.extClip, m.useExtClip = nil, false
	}
}

// startDialTransfer is startTransfer for a source that isn't open: the
// task connects to it first and disconnects when done.
func (m *Model) startDialTransfer(g extGroup, destFS vfs.FileSystem, destDir string, copyMode bool) {
	kind := TaskMove
	if copyMode {
		kind = TaskCopy
	}
	t := m.startTask(kind, len(g.names), func(prog *fileops.Progress) *fileops.Result {
		srcFS, err := g.src.dial()
		if err != nil {
			return &fileops.Result{Errors: []error{fmt.Errorf("connecting to %s: %w", g.src.label, err)}}
		}
		defer srcFS.Close()
		items := make([]fileops.Item, len(g.names))
		for i, n := range g.names {
			items[i] = fileops.Item{FS: srcFS, Path: srcFS.Join(g.dir, n)}
		}
		if copyMode {
			return fileops.Copy(items, destFS, destDir, prog)
		}
		return fileops.Move(items, destFS, destDir, prog)
	})
	t.Label = "from " + g.src.label
	m.dialog = Dialog{Kind: DialogProgress, Title: t.Kind.String(), TaskID: t.ID}
}

// effectiveClipboard is what a mirror paste (Ctrl+Alt+S) uses: the
// system clipboard's files when they're the latest copy — as long as
// they're in one folder of a source that's open (a mirror needs to
// identify both ends) — otherwise shfm's own clipboard.
func (m *Model) effectiveClipboard() (Clipboard, error) {
	if !m.useExtClip {
		return m.clipboard, nil
	}
	groups, err := m.extGroups()
	if err != nil {
		return Clipboard{}, err
	}
	if len(groups) != 1 || groups[0].src.fs == nil {
		return Clipboard{}, fmt.Errorf("open the copied items' source in a pane first (and copy from a single folder)")
	}
	g := groups[0]
	return Clipboard{FS: g.src.fs, Dir: g.dir, Names: g.names}, nil
}
