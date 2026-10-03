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
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/cloud"
	"shfm/internal/config"
	"shfm/internal/vfs"
)

// These tests run in both builds: without the cloud module there must be
// no trace of cloud sources; with it, they talk to no real service (an
// authorization is only started, never completed, and a saved account has
// no token).

// isolateCloudTest keeps the test off the user's configuration and away
// from their browser.
func isolateCloudTest(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
}

func requireCloud(t *testing.T) {
	t.Helper()
	if !cloud.Available {
		t.Skip("built without the cloud module")
	}
}

func TestCloudMenuEntries(t *testing.T) {
	isolateCloudTest(t)
	m := newTestModel()
	m.cfg.CloudSources = []config.CloudSource{
		{Name: "user@example.com", Provider: cloud.GoogleDrive, User: "user@example.com", Account: "a1"},
		{Name: "Work", Provider: cloud.OneDrive, User: "me@corp.example", Account: "a2"},
	}
	entries := m.cloudMenuEntries()
	if !cloud.Available {
		if len(entries) != 0 {
			t.Fatalf("cloud entries without the cloud module: %v", entries)
		}
		return
	}
	var labels []string
	for _, e := range entries {
		labels = append(labels, e.label)
		if sourceMenuSection(e.kind) != "Cloud" {
			t.Errorf("%q is in section %q", e.label, sourceMenuSection(e.kind))
		}
	}
	want := []string{
		iconSourceGDrive + " Google Drive  user@example.com",
		iconSourceOneDrive + " Microsoft OneDrive  me@corp.example  [Work]",
		iconSourceAdd + " New Google Drive account…",
		iconSourceAdd + " New Dropbox account…",
		iconSourceAdd + " New Microsoft OneDrive account…",
	}
	if strings.Join(labels, "\n") != strings.Join(want, "\n") {
		t.Errorf("entries:\n%s\nwant:\n%s", strings.Join(labels, "\n"), strings.Join(want, "\n"))
	}
}

// TestSourceMenuSections: the picker's sections, rendered with their
// titles; Cloud only with the cloud module.
func TestSourceMenuSections(t *testing.T) {
	isolateCloudTest(t)
	m := newTestModel()
	m.width, m.height = 120, 60
	// The entries openSourceMenu would list, without asking the system
	// for its disks and USB devices.
	m.sourceMenuEntries = append([]sourceMenuEntry{
		{label: iconSourceLocal + " /", kind: "local"},
		{label: iconSourceAdd + " New SMB connection\u2026", kind: "new-smb"},
	}, m.cloudMenuEntries()...)
	m.dialog = Dialog{Kind: DialogSourceMenu, Title: "Source"}
	for _, e := range m.sourceMenuEntries {
		m.dialog.Items = append(m.dialog.Items, e.label)
	}
	box := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(m.renderDialogBox(), "")
	for _, title := range []string{"Local", "Remote", "Cloud"} {
		if title == "Cloud" && !cloud.Available {
			continue
		}
		if !strings.Contains(box, title) {
			t.Errorf("no %q section title in:\n%s", title, box)
		}
	}
	if got := strings.Contains(box, "New Dropbox account"); got != cloud.Available {
		t.Errorf("cloud actions shown: %v, cloud module: %v", got, cloud.Available)
	}
}

// TestSaveCloudSource: an authorized account is saved among the cloud
// sources, never the remote ones; authorizing it again updates it,
// keeping the name the user gave it.
func TestSaveCloudSource(t *testing.T) {
	isolateCloudTest(t)
	m := newTestModel()
	acc := cloud.Account{Provider: cloud.Dropbox, ID: "id1", User: "user@example.com", ClientID: "key", ClientSecret: "s3cret"}
	m.saveCloudSource(acc, config.CloudSource{})
	if len(m.cfg.CloudSources) != 1 || len(m.cfg.RemoteSources) != 0 {
		t.Fatalf("cloud %v, remote %v", m.cfg.CloudSources, m.cfg.RemoteSources)
	}
	c := m.cfg.CloudSources[0]
	if c.Provider != cloud.Dropbox || c.User != acc.User || c.Account != "id1" || c.ClientID != "key" || c.Name != acc.User {
		t.Errorf("saved %+v", c)
	}
	if c.EncryptedClientSecret == "" || strings.Contains(c.EncryptedClientSecret, "s3cret") {
		t.Errorf("client secret saved as %q", c.EncryptedClientSecret)
	}
	if got, err := c.DecryptedClientSecret(); err != nil || got != "s3cret" {
		t.Errorf("DecryptedClientSecret = %q, %v", got, err)
	}

	m.cfg.CloudSources[0].Name = "Personal"
	acc.ClientSecret = ""
	m.saveCloudSource(acc, m.cfg.CloudSources[0])
	if len(m.cfg.CloudSources) != 1 || m.cfg.CloudSources[0].Name != "Personal" || m.cfg.CloudSources[0].EncryptedClientSecret != "" {
		t.Errorf("after authorizing again: %+v", m.cfg.CloudSources)
	}
	if loaded := config.Load(); len(loaded.CloudSources) != 1 || loaded.CloudSources[0].Name != "Personal" {
		t.Errorf("saved configuration: %+v", loaded.CloudSources)
	}
}

// TestCloudAccountForm: the form refuses what can't start an
// authorization, saying why.
func TestCloudAccountForm(t *testing.T) {
	requireCloud(t)
	isolateCloudTest(t)
	m := newTestModel()
	p, _ := cloud.Provider(cloud.GoogleDrive)
	m.openCloudAccountForm(p, config.CloudSource{}, "")
	if m.dialog.Kind != DialogConnectCloud || m.dialog.Title != "Add a Google Drive account" {
		t.Fatalf("dialog %v %q", m.dialog.Kind, m.dialog.Title)
	}
	if p.DefaultClientID == "" {
		m.confirmDialog()
		if m.dialog.Kind != DialogConnectCloud || !m.dialog.IsError || !strings.Contains(m.dialog.Message, "client ID") {
			t.Errorf("without a client ID: %v %q", m.dialog.Kind, m.dialog.Message)
		}
	}
	m.dialog.Inputs[0].SetValue("cid")
	m.confirmDialog()
	if m.dialog.Kind != DialogConnectCloud || !strings.Contains(m.dialog.Message, "secret") {
		t.Errorf("Google without the client secret: %v %q", m.dialog.Kind, m.dialog.Message)
	}
	m.width = 100
	if box := m.renderDialogBox(); !strings.Contains(box, "Redirect URI to register") {
		t.Errorf("form:\n%s", box)
	}
}

// TestCloudAuthorizationCancel: the authorization dialog shows the page
// to open, and Esc abandons the authorization.
func TestCloudAuthorizationCancel(t *testing.T) {
	requireCloud(t)
	isolateCloudTest(t)
	m := newTestModel()
	m.width = 120
	p, _ := cloud.Provider(cloud.OneDrive)
	m.openCloudAccountForm(p, config.CloudSource{}, "")
	m.dialog.Inputs[0].SetValue("client-id")
	m.confirmDialog()
	if m.dialog.Kind != DialogCloudAuth || m.dialog.CloudAuth == nil {
		t.Fatalf("dialog %v, message %q", m.dialog.Kind, m.dialog.Message)
	}
	if m.dialog.CloudBrowser {
		t.Error("tried to open a browser without a graphical session")
	}
	if box := m.renderDialogBox(); !strings.Contains(box, "login.microsoftonline.com") {
		t.Errorf("the authorization page isn't shown:\n%s", box)
	}

	m.updateDialogKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.dialog.Kind != DialogNone {
		t.Errorf("dialog after Esc: %v", m.dialog.Kind)
	}
	select {
	case msg := <-m.connectCh:
		m.handleConnectResult(msg)
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled authorization never ended")
	}
	if !strings.Contains(m.status, "cancelled") || m.dialog.Kind != DialogNone {
		t.Errorf("status %q, dialog %v", m.status, m.dialog.Kind)
	}
}

// TestConnectSavedCloudReauthorizes: a saved account whose authorization
// is gone opens the form to authorize it again.
func TestConnectSavedCloudReauthorizes(t *testing.T) {
	requireCloud(t)
	isolateCloudTest(t)
	m := newTestModel()
	src := config.CloudSource{Name: "user@example.com", Provider: cloud.Dropbox, User: "user@example.com", Account: "no-token", ClientID: "key"}
	m.connectSavedCloud(src)
	select {
	case msg := <-m.connectCh:
		m.handleConnectResult(msg)
	case <-time.After(10 * time.Second):
		t.Fatal("the connection attempt never ended")
	}
	if m.dialog.Kind != DialogConnectCloud || !strings.Contains(m.dialog.Message, "no longer valid") ||
		m.dialog.CloudSource.Account != "no-token" || m.dialog.Inputs[0].Value() != "key" {
		t.Errorf("dialog %v %q, account %q", m.dialog.Kind, m.dialog.Message, m.dialog.CloudSource.Account)
	}
}

// serviceTrashFS is a source whose service keeps what's deleted in a
// trash of its own, as the cloud ones do.
type serviceTrashFS struct{ vfs.FileSystem }

func (serviceTrashFS) TrashName() string { return "Test trash" }

// TestDeleteOnServiceTrash: deleting on such a source, with either key,
// says where the items go, and doesn't claim it can't be undone.
func TestDeleteOnServiceTrash(t *testing.T) {
	isolateCloudTest(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestModel()
	m.panes[m.active] = NewPane(serviceTrashFS{vfs.NewLocalFS("test", dir)}, dir, false, m.active, m.sizeCh)
	p := m.activePane()
	p.ToggleSelectName("f.txt")
	for _, useTrash := range []bool{true, false} {
		m.askDelete(useTrash)
		if m.dialog.Kind != DialogConfirmTrash || !strings.Contains(m.dialog.Message, "Test trash") {
			t.Errorf("useTrash %v: %v %q", useTrash, m.dialog.Kind, m.dialog.Message)
		}
	}
}

// TestRemoveSavedSources: x in the source picker asks to remove the
// highlighted saved source or account, and only those; removing one
// drops it from the saved configuration.
func TestRemoveSavedSources(t *testing.T) {
	isolateCloudTest(t)
	m := newTestModel()
	nas := config.RemoteSource{Name: "nas/share", Kind: "smb", Host: "nas", Share: "share"}
	box := config.RemoteSource{Name: "me@box", Kind: "sftp", Host: "box", User: "me"}
	acc := config.CloudSource{Name: "u@example.com", Provider: cloud.Dropbox, User: "u@example.com", Account: "acc1"}
	m.cfg.RemoteSources = []config.RemoteSource{nas, box}
	m.cfg.CloudSources = []config.CloudSource{acc}
	m.sourceMenuEntries = []sourceMenuEntry{
		{label: "/", kind: "local"},
		{label: "nas", kind: "remote", remote: nas},
		{label: "box", kind: "remote", remote: box},
		{label: "acc", kind: "cloud", cloud: acc},
		{label: "new", kind: "new-smb"},
	}
	menu := func(idx int) {
		m.dialog = Dialog{Kind: DialogSourceMenu, Items: make([]string, len(m.sourceMenuEntries)), ItemIdx: idx}
	}

	for _, idx := range []int{0, 4} {
		menu(idx)
		m.updateDialogKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
		if m.dialog.Kind == DialogConfirmRemoveSource {
			t.Errorf("entry %d (%s) offered for removal", idx, m.sourceMenuEntries[idx].kind)
		}
	}

	menu(1)
	m.updateDialogKey(tea.KeyPressMsg{Code: tea.KeyDelete})
	if m.dialog.Kind != DialogConfirmRemoveSource || !strings.Contains(m.dialog.Message, "nas/share") {
		t.Fatalf("dialog %v %q", m.dialog.Kind, m.dialog.Message)
	}
	m.removeRemoteSource(m.dialog.RemoveSource.remote)
	if len(m.cfg.RemoteSources) != 1 || m.cfg.RemoteSources[0] != box {
		t.Errorf("remote sources after removal: %+v", m.cfg.RemoteSources)
	}
	if saved := config.Load(); len(saved.RemoteSources) != 1 {
		t.Errorf("saved remote sources: %+v", saved.RemoteSources)
	}

	menu(3)
	m.updateDialogKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.dialog.Kind != DialogConfirmRemoveSource || !strings.Contains(m.dialog.Message, "dropbox://u@example.com") {
		t.Fatalf("dialog %v %q", m.dialog.Kind, m.dialog.Message)
	}
	if !cloud.Available {
		return
	}
	m.removeCloudSource(m.dialog.RemoveSource.cloud)
	if len(m.cfg.CloudSources) != 0 || m.statusErr {
		t.Errorf("cloud sources after removal: %+v (status %q)", m.cfg.CloudSources, m.status)
	}
	if saved := config.Load(); len(saved.CloudSources) != 0 || len(saved.RemoteSources) != 1 {
		t.Errorf("saved: cloud %+v, remote %+v", saved.CloudSources, saved.RemoteSources)
	}
}

// labeledFS is a source with the label and kind of a saved one.
type labeledFS struct {
	vfs.FileSystem
	label string
}

func (l labeledFS) Label() string  { return l.label }
func (l labeledFS) Kind() vfs.Kind { return vfs.KindSMB }

// TestRemoveOpenSourceRefused: a saved source open in a pane can't be
// removed until the pane switches away from it.
func TestRemoveOpenSourceRefused(t *testing.T) {
	isolateCloudTest(t)
	dir := t.TempDir()
	m := newTestModel()
	nas := config.RemoteSource{Name: "nas/share", Kind: "smb", Host: "nas", Share: "share"}
	m.cfg.RemoteSources = []config.RemoteSource{nas}
	m.panes[1] = NewPane(labeledFS{vfs.NewLocalFS("x", dir), "smb://nas/share"}, dir, false, 1, m.sizeCh)
	m.sourceMenuEntries = []sourceMenuEntry{{label: "nas", kind: "remote", remote: nas}}
	m.dialog = Dialog{Kind: DialogSourceMenu, Items: []string{"nas"}}
	m.updateDialogKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.dialog.Kind == DialogConfirmRemoveSource || !m.statusErr ||
		!strings.Contains(m.status, "right pane") || !strings.Contains(m.status, "close it (Ctrl+L") {
		t.Errorf("dialog %v, status %q", m.dialog.Kind, m.status)
	}
	if len(m.cfg.RemoteSources) != 1 {
		t.Error("an open source was removed")
	}

	// In single-pane mode, the hidden pane counts as closed: the removal
	// goes ahead, taking that pane back to the home folder.
	m.dualPane, m.active = false, 0
	m.dialog = Dialog{Kind: DialogSourceMenu, Items: []string{"nas"}}
	m.updateDialogKey(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.dialog.Kind != DialogConfirmRemoveSource || m.dialog.RemoveHiddenPane != 1 ||
		!strings.Contains(m.dialog.Message, "hidden right pane") {
		t.Fatalf("dialog %v %q, hidden pane %d", m.dialog.Kind, m.dialog.Message, m.dialog.RemoveHiddenPane)
	}
	m.removeRemoteSource(m.dialog.RemoveSource.remote)
	home := homeOrRoot()
	m.replaceFS(m.dialog.RemoveHiddenPane, vfs.NewLocalFS("Local", home), home)
	if len(m.cfg.RemoteSources) != 0 || m.panes[1].FS.Kind() != vfs.KindLocal || m.panes[1].Path != home {
		t.Errorf("remote %v, right pane %v %s", m.cfg.RemoteSources, m.panes[1].FS.Kind(), m.panes[1].Path)
	}
}
