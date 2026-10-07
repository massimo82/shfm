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

//go:build vault

package ui

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/config"
	"shfm/internal/drives"
	"shfm/internal/vault"
	"shfm/internal/vfs"
)

const testVaultPW = "a long enough password"

// typeInto sets the dialog's fields.
func typeInto(m *Model, values ...string) {
	for i, v := range values {
		m.dialog.Inputs[i].SetValue(v)
	}
}

// run applies cmd's message, as the event loop would.
func run(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatalf("no command (dialog %v: %q)", m.dialog.Kind, m.dialog.Message)
	}
	m.Update(cmd())
}

func key(m *Model, k string) {
	switch k {
	case "ctrl+r":
		m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	case "ctrl+alt+l":
		m.Update(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl | tea.ModAlt})
	case "enter":
		m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	}
}

// createVault goes through "New…" → "New encrypted vault" in dir and
// returns the model, showing the new vault, and its recovery key.
func createVault(t *testing.T, dir, name string) (*Model, string) {
	t.Helper()
	m := archiveTestModel(t, dir)
	m.openNewItemChoice()
	if len(m.dialog.Items) != 3 {
		t.Fatalf("New… offers %v", m.dialog.Items)
	}
	m.dialog.ItemIdx = 2
	m.confirmDialog()
	if m.dialog.Kind != DialogNewVault {
		t.Fatalf("dialog %v, want DialogNewVault", m.dialog.Kind)
	}

	typeInto(m, name, "short", "short")
	if cmd, _ := m.confirmDialog(); cmd != nil || !m.dialog.IsError {
		t.Fatal("a short password was accepted")
	}
	typeInto(m, name, testVaultPW, testVaultPW+"x")
	if cmd, _ := m.confirmDialog(); cmd != nil || !m.dialog.IsError {
		t.Fatal("two different passwords were accepted")
	}
	typeInto(m, name, testVaultPW, testVaultPW)
	cmd, _ := m.confirmDialog()
	run(t, m, cmd)
	if m.dialog.Kind != DialogVaultRecoveryKey {
		t.Fatalf("dialog %v (%q), want the recovery key", m.dialog.Kind, m.status)
	}
	recovery := m.dialog.VaultKey
	if !strings.HasPrefix(recovery, "AGE-SECRET-KEY-1") {
		t.Fatalf("recovery key %q", recovery)
	}
	key(m, "enter") // "I have saved it"
	if m.dialog.Kind != DialogNone {
		t.Fatalf("dialog %v still open", m.dialog.Kind)
	}
	return m, recovery
}

func TestVaultCreateEnterLeave(t *testing.T) {
	dir := t.TempDir()
	m, _ := createVault(t, dir, "Private")
	p := m.activePane()
	if p.FS.Kind() != vfs.KindVault || p.Path != "/" || p.VaultExit == nil {
		t.Fatalf("the pane doesn't show the new vault: %v %q", p.FS.Kind(), p.Path)
	}
	if !vault.IsVault(vfs.NewLocalFS("x", "/"), filepath.Join(dir, "Private")) {
		t.Fatal("no vault on disk")
	}

	// Writing through the pane's FS encrypts.
	w, _ := p.FS.Create("/secret.txt")
	w.Write([]byte("top secret"))
	w.Close()
	p.Load()
	if len(p.Entries) != 2 || p.Entries[0].Name != parentEntryName || p.Entries[1].Name != "secret.txt" {
		t.Fatalf("vault root lists %v", p.Entries)
	}

	// ".." at the vault's root: back to the folder holding it.
	p.Cursor = 0
	m.enterOrOpen()
	if p.FS.Kind() != vfs.KindLocal || p.Path != dir || p.VaultExit != nil {
		t.Fatalf("after ..: %v %q", p.FS.Kind(), p.Path)
	}
	if e, _ := p.CurrentEntry(); e.Name != "Private" {
		t.Fatalf("cursor on %q, want the vault", e.Name)
	}

	// Unlocked for the session: entering again asks nothing.
	m.enterOrOpen()
	if m.dialog.Kind != DialogNone || p.FS.Kind() != vfs.KindVault {
		t.Fatalf("re-entering: dialog %v, kind %v", m.dialog.Kind, p.FS.Kind())
	}
}

func TestVaultLockAndUnlock(t *testing.T) {
	dir := t.TempDir()
	m, recovery := createVault(t, dir, "Box")
	p := m.activePane()
	vaultFS := p.FS
	m.clipboard = Clipboard{FS: vaultFS, Dir: "/", Names: []string{"x"}}

	key(m, "ctrl+alt+l")
	if p.VaultExit != nil || p.FS.Kind() != vfs.KindLocal {
		t.Fatal("the pane still shows the locked vault")
	}
	if !m.clipboard.Empty() {
		t.Fatal("the clipboard still holds entries of the locked vault")
	}
	if _, err := vaultFS.List("/"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("the vault's FS after locking: %v", err)
	}

	// Locked: entering asks for the password.
	m.enterOrOpen()
	if m.dialog.Kind != DialogVaultUnlock {
		t.Fatalf("dialog %v, want DialogVaultUnlock", m.dialog.Kind)
	}
	typeInto(m, "wrong password")
	cmd, _ := m.confirmDialog()
	run(t, m, cmd)
	if m.dialog.Kind != DialogVaultUnlock || m.dialog.Message != "Wrong password" {
		t.Fatalf("wrong password: dialog %v %q", m.dialog.Kind, m.dialog.Message)
	}

	// Ctrl+R: the recovery key instead.
	key(m, "ctrl+r")
	if !m.dialog.VaultUseKey {
		t.Fatal("Ctrl+R didn't switch to the recovery key")
	}
	typeInto(m, "  "+recovery+"\n")
	cmd, _ = m.confirmDialog()
	run(t, m, cmd)
	if m.dialog.Kind != DialogNone || p.FS.Kind() != vfs.KindVault {
		t.Fatalf("unlock with the key: dialog %v %q, kind %v", m.dialog.Kind, m.dialog.Message, p.FS.Kind())
	}
}

func TestVaultAutoLock(t *testing.T) {
	m, _ := createVault(t, t.TempDir(), "Auto")
	m.cfg.VaultAutoLockMinutes = 5

	m.lastInput = time.Now().Add(-4 * time.Minute)
	if cmd := m.handleVaultTick(); cmd == nil || len(m.vaults) != 1 {
		t.Fatal("locked before the time")
	}
	m.lastInput = time.Now().Add(-6 * time.Minute)
	m.handleVaultTick()
	if len(m.vaults) != 0 || m.activePane().VaultExit != nil {
		t.Fatal("not locked after the time")
	}

	// 0: never on its own.
	m2, _ := createVault(t, t.TempDir(), "Never")
	m2.cfg.VaultAutoLockMinutes = 0
	m2.lastInput = time.Now().Add(-48 * time.Hour)
	m2.handleVaultTick()
	if len(m2.vaults) != 1 {
		t.Fatal("locked with auto-lock off")
	}
}

func TestVaultPasswordAndRecoveryKey(t *testing.T) {
	dir := t.TempDir()
	m, recovery := createVault(t, dir, "Keys")

	var labels []string
	m.openSourceMenu()
	for _, e := range m.sourceMenuEntries {
		if sourceMenuSection(e.kind) == "Vaults" {
			labels = append(labels, e.label)
		}
	}
	if len(labels) != 4 || !strings.Contains(labels[3], "New encrypted vault") {
		t.Fatalf("Vaults section: %v", labels)
	}

	m.dialog = Dialog{}
	m.askVaultShowKey()
	typeInto(m, testVaultPW)
	cmd, _ := m.confirmDialog()
	run(t, m, cmd)
	if m.dialog.Kind != DialogVaultRecoveryKey || m.dialog.VaultKey != recovery {
		t.Fatalf("show key: dialog %v", m.dialog.Kind)
	}
	m.closeRecoveryKey()

	m.askVaultPassword()
	typeInto(m, "not it", "a brand new password", "a brand new password")
	cmd, _ = m.confirmDialog()
	run(t, m, cmd)
	if !m.statusErr {
		t.Fatalf("changed with a wrong current password: %q", m.status)
	}
	m.askVaultPassword()
	typeInto(m, testVaultPW, "a brand new password", "a brand new password")
	cmd, _ = m.confirmDialog()
	run(t, m, cmd)
	if m.statusErr {
		t.Fatalf("change: %q", m.status)
	}
	be := vfs.NewLocalFS("x", "/")
	if _, err := vault.Unlock(be, filepath.Join(dir, "Keys"), "a brand new password"); err != nil {
		t.Fatalf("the new password: %v", err)
	}
}

// A file opened with an application through a temp copy is decrypted in
// memory only — never without vault.SpoolDir.
func TestVaultTempCopyStaysInMemory(t *testing.T) {
	m, _ := createVault(t, t.TempDir(), "Tmp")
	fs := m.activePane().FS
	w, _ := fs.Create("/doc.txt")
	w.Write([]byte("clear"))
	w.Close()

	spool := t.TempDir()
	prev := vault.SpoolDir
	t.Cleanup(func() { vault.SpoolDir = prev })
	vault.SpoolDir = func() (string, bool) { return spool, true }
	p, err := downloadToTemp(fs, "/doc.txt", "doc.txt")
	if err != nil || !strings.HasPrefix(p, spool) {
		t.Fatalf("temp copy at %q (%v), want under %s", p, err, spool)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "clear" {
		t.Fatalf("temp copy holds %q", data)
	}

	vault.SpoolDir = func() (string, bool) { return "", false }
	if _, err := downloadToTemp(fs, "/doc.txt", "doc.txt"); !errors.Is(err, errNoVaultSpool) {
		t.Fatalf("without a spool: %v", err)
	}
}

// Copies with shfm's own transfer, into and out of the vault.
func TestVaultTransfer(t *testing.T) {
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "plain.txt"), []byte("plain"), 0o644)
	m, _ := createVault(t, t.TempDir(), "Copy")
	vfsys := m.activePane().FS
	m.startTransfer(vfs.NewLocalFS("Local", "/"), outside, []string{"plain.txt"}, vfsys, "/", true)
	if tk := waitTask(t, m); tk.ErrorCount > 0 {
		t.Fatalf("into the vault: %v", tk.LastError)
	}
	r, err := vfsys.Open("/plain.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if string(got) != "plain" {
		t.Fatalf("in the vault: %q", got)
	}
	out := t.TempDir()
	m.startTransfer(vfsys, "/", []string{"plain.txt"}, vfs.NewLocalFS("Local", "/"), out, true)
	if !strings.Contains(m.status, "not encrypted") {
		t.Fatalf("no notice copying out of the vault: %q", m.status)
	}
	if tk := waitTask(t, m); tk.ErrorCount > 0 {
		t.Fatalf("out of the vault: %v", tk.LastError)
	}
}

func TestLockVaultsKeyBinding(t *testing.T) {
	if a, ok := config.DefaultKeyMap().ActionFor("ctrl+alt+l"); !ok || a != config.ActionLockVaults {
		t.Fatalf("ctrl+alt+l → %v", a)
	}
}

// splitForm fills the split vault form: three local folders, named name
// in the three locations.
func splitForm(t *testing.T, m *Model, name string, locs [3]string, pw, again string) tea.Cmd {
	t.Helper()
	m.openSourceMenu()
	for i, e := range m.sourceMenuEntries {
		if e.kind == "new-vault" {
			m.dialog.ItemIdx = i
		}
	}
	m.confirmDialog()
	if m.dialog.Kind != DialogNewVault {
		t.Fatalf("dialog %v, want the new vault form (in a folder: the default)", m.dialog.Kind)
	}
	typeInto(m, name, pw, again)
	m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if m.dialog.Kind != DialogNewSplitVault {
		t.Fatalf("Ctrl+T: dialog %v, want the split vault form", m.dialog.Kind)
	}
	if m.dialog.Inputs[0].Value() != name || m.dialog.Inputs[4].Value() != pw || m.dialog.Inputs[5].Value() != again {
		t.Fatal("Ctrl+T lost what was typed")
	}
	if m.dialog.SplitChoices[m.dialog.SplitChoice[0]].id != "local" {
		t.Fatal("part 1 doesn't default to the local disk")
	}
	m.dialog.SplitChoice = [3]int{0, 0, 0} // all local, in three folders
	typeInto(m, name, locs[0], locs[1], locs[2], pw, again)
	cmd, _ := m.confirmDialog()
	return cmd
}

func TestSplitVaultUI(t *testing.T) {
	home := t.TempDir()
	m := archiveTestModel(t, home)
	var locs, dirs [3]string
	for i := range dirs {
		locs[i] = t.TempDir()
		dirs[i] = filepath.Join(locs[i], "Split")
	}

	// Creating: the folders are made, the key shown, the vault opened.
	run(t, m, splitForm(t, m, "Split", locs, testVaultPW, testVaultPW))
	if m.dialog.Kind != DialogVaultRecoveryKey {
		t.Fatalf("dialog %v %q, want the recovery key", m.dialog.Kind, m.dialog.Message)
	}
	key(m, "enter")
	p := m.activePane()
	if p.FS.Kind() != vfs.KindVault || p.VaultExit == nil || !p.VaultExit.Standalone {
		t.Fatalf("the pane doesn't show the split vault: %v", p.FS.Kind())
	}
	if len(p.Entries) != 0 {
		t.Fatalf("a standalone vault's root lists %v (no ..)", p.Entries)
	}
	if len(m.cfg.SplitVaults) != 1 || m.cfg.SplitVaults[0].Parts[1].Path != dirs[1] {
		t.Fatalf("the split vault wasn't saved: %+v", m.cfg.SplitVaults)
	}
	w, _ := p.FS.Create("/doc.txt")
	w.Write([]byte("in three parts"))
	w.Close()
	for i := range dirs {
		if _, err := os.Stat(filepath.Join(dirs[i], "RECOVERY.txt")); err != nil {
			t.Fatalf("part %d has no RECOVERY.txt: %v", i+1, err)
		}
	}

	// Locking: the pane goes back home, the parts are closed.
	key(m, "ctrl+alt+l")
	if p := m.activePane(); p.FS.Kind() != vfs.KindLocal {
		t.Fatalf("after locking the pane shows %v", p.FS.Kind())
	}

	// Part 2 lost: opened from the picker, read-only, still readable.
	os.RemoveAll(dirs[1])
	m.openSourceMenu()
	for i, e := range m.sourceMenuEntries {
		if e.kind == "split-vault" {
			m.dialog.ItemIdx = i
		}
	}
	m.confirmDialog()
	cmd := m.queued[len(m.queued)-1]
	m.queued = nil
	run(t, m, cmd)
	if m.dialog.Kind != DialogVaultUnlock {
		t.Fatalf("dialog %v (%q), want the password", m.dialog.Kind, m.status)
	}
	typeInto(m, testVaultPW)
	cmd, _ = m.confirmDialog()
	run(t, m, cmd)
	p = m.activePane()
	if p.FS.Kind() != vfs.KindVault || !m.statusErr || !strings.Contains(m.status, "read-only") {
		t.Fatalf("degraded open: %v, status %q", p.FS.Kind(), m.status)
	}
	r, err := p.FS.Open("/doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if string(got) != "in three parts" {
		t.Fatalf("read with two parts: %q", got)
	}
	if w, err := p.FS.Create("/new"); err == nil {
		w.Write([]byte("x"))
		if err := w.Close(); err == nil {
			t.Fatal("wrote with a part missing")
		}
	}
	if _, err := p.FS.Stat("/new"); err == nil {
		t.Fatal("a failed write left a file")
	}

	// Part 2 back, empty: repair rebuilds it.
	os.MkdirAll(dirs[1], 0o755)
	key(m, "ctrl+alt+l")
	m.openSourceMenu()
	for i, e := range m.sourceMenuEntries {
		if e.kind == "split-vault" {
			m.dialog.ItemIdx = i
		}
	}
	m.confirmDialog()
	cmd = m.queued[len(m.queued)-1]
	m.queued = nil
	run(t, m, cmd)
	typeInto(m, testVaultPW)
	cmd, _ = m.confirmDialog()
	run(t, m, cmd)
	m.repairSplitVault()
	if tk := waitTask(t, m); tk.ErrorCount > 0 {
		t.Fatalf("repair: %v", tk.LastError)
	}
	if !strings.Contains(m.status, "repaired") {
		t.Fatalf("after repair: %q", m.status)
	}
	if entries, _ := os.ReadDir(dirs[1]); len(entries) < 3 {
		t.Fatalf("part 2 after repair holds %d entries", len(entries))
	}

	// Forgotten, then added back with the same folders and the password.
	m.dialog = Dialog{} // the repair's progress
	key(m, "ctrl+alt+l")
	m.removeSplitVault(m.cfg.SplitVaults[0])
	if len(m.cfg.SplitVaults) != 0 {
		t.Fatal("not forgotten")
	}
	run(t, m, splitForm(t, m, "Split", locs, testVaultPW, ""))
	if m.dialog.Kind != DialogNone || m.activePane().FS.Kind() != vfs.KindVault {
		t.Fatalf("adding back: dialog %v %q", m.dialog.Kind, m.dialog.Message)
	}
}

// A new split vault needs empty folders.
func TestSplitVaultNeedsEmptyFolders(t *testing.T) {
	m := archiveTestModel(t, t.TempDir())
	var locs [3]string
	for i := range locs {
		locs[i] = t.TempDir()
	}
	os.Mkdir(filepath.Join(locs[2], "S"), 0o755)
	os.WriteFile(filepath.Join(locs[2], "S", "unrelated"), nil, 0o644)
	run(t, m, splitForm(t, m, "S", locs, testVaultPW, testVaultPW))
	if m.dialog.Kind != DialogNewSplitVault || !strings.Contains(m.dialog.Message, "isn't empty") {
		t.Fatalf("dialog %v %q", m.dialog.Kind, m.dialog.Message)
	}
	if entries, _ := os.ReadDir(locs[0]); len(entries) != 0 {
		t.Fatal("something was written to part 1")
	}

	// The same folder twice, and a name that can't be a folder's.
	run(t, m, splitForm(t, m, "T", [3]string{locs[0], locs[0], locs[1]}, testVaultPW, testVaultPW))
	if !strings.Contains(m.dialog.Message, "same folder") {
		t.Fatalf("same folder twice: %q", m.dialog.Message)
	}
	if cmd := splitForm(t, m, "a/b", locs, testVaultPW, testVaultPW); cmd != nil || !strings.Contains(m.dialog.Message, "No /") {
		t.Fatalf("a name with a /: %q", m.dialog.Message)
	}
}

// A part on a removable disk, found by its filesystem UUID wherever it's
// mounted (mounted when needed), and one on the local disk by default in
// the home folder.
func TestSplitVaultRemovableDisk(t *testing.T) {
	m := archiveTestModel(t, t.TempDir())
	home, _ := os.UserHomeDir() // archiveTestModel's
	stick := t.TempDir()        // the disk's content
	mounted, mountedAt, mounts := false, "", 0
	listRemovableDrives = func() ([]drives.RemovableDevice, error) {
		return []drives.RemovableDevice{{Name: "sdz1", Path: "/dev/sdz1", SizeBytes: 1 << 30, Vendor: "Acme", Model: "Stick"}}, nil
	}
	uuidOf = func(dev string) string {
		if dev == "/dev/sdz1" {
			return "AB12-CD34"
		}
		return ""
	}
	attached := true
	deviceByUUID = func(uuid string) (string, bool) { return "/dev/sdz1", attached && uuid == "AB12-CD34" }
	mountByUUID = func(uuid string) (drives.Mount, bool) {
		return drives.Mount{MountPoint: mountedAt, UUID: uuid}, mounted && uuid == "AB12-CD34"
	}
	autoMount = func(dev, name string) (string, error) {
		mounts++
		// Mounted somewhere else every time: a link to the disk's content.
		mountedAt = filepath.Join(t.TempDir(), name)
		if err := os.Symlink(stick, mountedAt); err != nil {
			return "", err
		}
		mounted = true
		return mountedAt, nil
	}
	t.Cleanup(func() {
		listRemovableDrives = func() ([]drives.RemovableDevice, error) { return nil, nil }
		uuidOf = func(string) string { return "" }
		mountByUUID = func(string) (drives.Mount, bool) { return drives.Mount{}, false }
		deviceByUUID = func(string) (string, bool) { return "", false }
		autoMount = func(string, string) (string, error) { return "", errors.New("no mounting in tests") }
	})

	m.askNewSplitVault("")
	d := &m.dialog
	if len(d.SplitChoices) < 2 || d.SplitChoices[1].id != "uuid:AB12-CD34" || !strings.Contains(d.SplitChoices[1].label, "Acme Stick") {
		t.Fatalf("the removable disk isn't offered: %+v", d.SplitChoices)
	}
	if d.Inputs[1].Placeholder != "in your home folder" || d.Inputs[2].Placeholder != "in the root of the source" {
		t.Fatalf("placeholders %q, %q", d.Inputs[1].Placeholder, d.Inputs[2].Placeholder)
	}
	other := t.TempDir()
	d.SplitChoice = [3]int{0, 1, 0} // home, the disk's root, a local folder
	typeInto(m, "Vault", "", "", other, testVaultPW, testVaultPW)
	cmd, _ := m.confirmDialog()
	run(t, m, cmd)
	if m.dialog.Kind != DialogVaultRecoveryKey {
		t.Fatalf("dialog %v %q, want the recovery key", m.dialog.Kind, m.dialog.Message)
	}
	key(m, "enter")
	for _, dir := range []string{filepath.Join(home, "Vault"), filepath.Join(stick, "Vault"), filepath.Join(other, "Vault")} {
		if _, err := os.Stat(filepath.Join(dir, "RECOVERY.txt")); err != nil {
			t.Fatalf("no part in %s: %v", dir, err)
		}
	}
	if mounts != 1 {
		t.Fatalf("mounted %d times, want once", mounts)
	}
	saved := m.cfg.SplitVaults[0].Parts
	if saved[0].Path != filepath.Join(home, "Vault") || saved[1] != (config.VaultPart{Source: "uuid:AB12-CD34", Path: "/Vault", Label: "Acme Stick"}) {
		t.Fatalf("saved parts %+v", saved)
	}
	w, _ := m.activePane().FS.Create("/doc.txt")
	w.Write([]byte("on a stick"))
	w.Close()
	key(m, "ctrl+alt+l")

	// Unplugged, its label still shown: the vault opens read-only.
	mounted, attached = false, false
	listRemovableDrives = func() ([]drives.RemovableDevice, error) { return nil, nil }
	entries := m.splitVaultMenuEntries()
	if !strings.Contains(entries[0].label, "Acme Stick") {
		t.Fatalf("picker entry %q", entries[0].label)
	}
	run(t, m, m.openSplitVault(m.cfg.SplitVaults[0], nil))
	typeInto(m, testVaultPW)
	cmd, _ = m.confirmDialog()
	run(t, m, cmd)
	if !strings.Contains(m.status, "part 2") || !strings.Contains(m.status, "read-only") {
		t.Fatalf("unplugged: %q", m.status)
	}
	key(m, "ctrl+alt+l")

	// Plugged back in: mounted elsewhere, found again.
	attached = true
	run(t, m, m.openSplitVault(m.cfg.SplitVaults[0], nil))
	typeInto(m, testVaultPW)
	cmd, _ = m.confirmDialog()
	run(t, m, cmd)
	if mounts != 2 || m.statusErr {
		t.Fatalf("plugged back: %d mounts, status %q", mounts, m.status)
	}
	r, err := m.activePane().FS.Open("/doc.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if string(got) != "on a stick" {
		t.Fatalf("read back %q", got)
	}
}

func TestAboutMentionsVaults(t *testing.T) {
	if !strings.Contains(aboutDescription(), "encrypted vaults") {
		t.Fatalf("About doesn't mention the vaults: %s", aboutDescription())
	}
}

// Ctrl+T goes back and forth between the two forms, the simple one first.
func TestVaultFormSwitch(t *testing.T) {
	m := archiveTestModel(t, t.TempDir())
	m.askNewVault()
	m.dialog.VaultPQ = true
	typeInto(m, "Name", "password1", "password1")
	m.switchVaultForm()
	m.switchVaultForm()
	d := m.dialog
	if d.Kind != DialogNewVault || !d.VaultPQ || d.Inputs[0].Value() != "Name" || d.Inputs[2].Value() != "password1" {
		t.Fatalf("after two switches: %v %v %q", d.Kind, d.VaultPQ, d.Inputs[0].Value())
	}
}

// No vault inside a vault: not offered, refused, and the source picker
// offers the split vault (in place of this one) only.
func TestNoVaultInsideVault(t *testing.T) {
	m, _ := createVault(t, t.TempDir(), "Outer")
	m.openNewItemChoice()
	for _, it := range m.dialog.Items {
		if strings.Contains(it, "vault") {
			t.Fatalf("New… inside a vault offers %v", m.dialog.Items)
		}
	}
	m.dialog = Dialog{}
	m.askNewVault()
	if m.dialog.Kind != DialogNone || !strings.Contains(m.status, "inside another vault") {
		t.Fatalf("askNewVault inside a vault: dialog %v, status %q", m.dialog.Kind, m.status)
	}
	m.openSourceMenu()
	for i, e := range m.sourceMenuEntries {
		if e.kind == "new-vault" {
			m.dialog.ItemIdx = i
		}
	}
	m.confirmDialog()
	if m.dialog.Kind != DialogNewSplitVault {
		t.Fatalf("source picker inside a vault: dialog %v, want the split form", m.dialog.Kind)
	}
	m.switchVaultForm()
	if m.dialog.Kind != DialogNewSplitVault || !m.dialog.IsError {
		t.Fatalf("Ctrl+T inside a vault: dialog %v", m.dialog.Kind)
	}
}

// Copying a vault into a vault is refused: at once when it's one of the
// items, from the task when it's deeper.
func TestNoVaultCopiedIntoVault(t *testing.T) {
	outside := t.TempDir()
	be := vfs.NewLocalFS("Local", "/")
	other := filepath.Join(outside, "Other")
	os.Mkdir(other, 0o755)
	if _, err := vault.Create(be, other, testVaultPW, vault.Options{}); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(outside, "folder", "sub"), 0o755)
	if _, err := vault.Create(be, filepath.Join(outside, "folder", "sub"), testVaultPW, vault.Options{}); err != nil {
		t.Fatal(err)
	}
	m, _ := createVault(t, t.TempDir(), "Target")
	vfsys := m.activePane().FS

	m.startTransfer(be, outside, []string{"Other"}, vfsys, "/", true)
	if len(m.tasks) != 0 || !strings.Contains(m.status, "can't be stored inside another vault") {
		t.Fatalf("top-level vault: %d tasks, status %q", len(m.tasks), m.status)
	}
	m.startTransfer(be, outside, []string{"folder"}, vfsys, "/", false)
	tk := waitTask(t, m)
	if tk.ErrorCount == 0 || !errors.Is(tk.LastError, vault.ErrVaultInVault) {
		t.Fatalf("nested vault: %v", tk.LastError)
	}
	if _, err := os.Stat(filepath.Join(outside, "folder", "sub", "vault.json")); err != nil {
		t.Fatal("the refused move removed the source")
	}
	if es, _ := vfsys.List("/"); len(es) != 0 {
		t.Fatalf("something was copied: %v", es)
	}
}

// Enter on a part's folder opens the split vault like a vault in a
// folder: its password, then its content; ".." and locking come back.
func TestSplitVaultEnterPart(t *testing.T) {
	m := archiveTestModel(t, t.TempDir())
	var locs [3]string
	for i := range locs {
		locs[i] = t.TempDir()
	}
	run(t, m, splitForm(t, m, "Split", locs, testVaultPW, testVaultPW))
	key(m, "enter") // the recovery key
	w, _ := m.activePane().FS.Create("/doc.txt")
	w.Write([]byte("x"))
	w.Close()
	key(m, "ctrl+alt+l")

	enterPart := func() {
		t.Helper()
		m.replaceActiveFS(vfs.NewLocalFS("Local", "/"), locs[1])
		moveCursorTo(t, m.activePane(), "Split")
		m.enterOrOpen()
		if len(m.queued) == 0 {
			t.Fatalf("Enter on the part: nothing to connect (dialog %v, status %q)", m.dialog.Kind, m.status)
		}
		cmd := m.queued[len(m.queued)-1]
		m.queued = nil
		run(t, m, cmd)
	}
	enterPart()
	if m.dialog.Kind != DialogVaultUnlock {
		t.Fatalf("dialog %v (%q), want the password", m.dialog.Kind, m.status)
	}
	typeInto(m, testVaultPW)
	cmd, _ := m.confirmDialog()
	run(t, m, cmd)
	p := m.activePane()
	if p.FS.Kind() != vfs.KindVault || p.VaultExit == nil || p.VaultExit.Back == nil {
		t.Fatalf("the pane doesn't show the vault: %v", p.FS.Kind())
	}
	if len(p.Entries) != 2 || !IsParentEntry(p.Entries[0]) || p.Entries[1].Name != "doc.txt" {
		t.Fatalf("the vault lists %v, want .. and doc.txt", p.Entries)
	}

	// "..": back to the part's folder, on the pane's source.
	p.Cursor = 0
	m.enterOrOpen()
	p = m.activePane()
	if p.FS.Kind() != vfs.KindLocal || p.Path != locs[1] || p.VaultExit != nil {
		t.Fatalf("after ..: %v at %s", p.FS.Kind(), p.Path)
	}
	if e, _ := p.CurrentEntry(); e.Name != "Split" {
		t.Fatalf("cursor on %q", e.Name)
	}

	// Unlocked already: no password; locking comes back too.
	enterPart()
	if m.dialog.Kind != DialogNone || m.activePane().FS.Kind() != vfs.KindVault {
		t.Fatalf("entering again: dialog %v", m.dialog.Kind)
	}
	key(m, "ctrl+alt+l")
	if p := m.activePane(); p.FS.Kind() != vfs.KindLocal || p.Path != locs[1] {
		t.Fatalf("after locking: %v at %s", p.FS.Kind(), p.Path)
	}

	// Not saved here: the form to add it, this part filled in.
	m.removeSplitVault(m.cfg.SplitVaults[0])
	m.replaceActiveFS(vfs.NewLocalFS("Local", "/"), locs[1])
	moveCursorTo(t, m.activePane(), "Split")
	m.enterOrOpen()
	d := m.dialog
	if d.Kind != DialogNewSplitVault || d.Inputs[0].Value() != "Split" || d.Inputs[2].Value() != locs[1] || d.SplitChoices[d.SplitChoice[1]].id != "local" {
		t.Fatalf("form %v: name %q, part 2 at %q", d.Kind, d.Inputs[0].Value(), d.Inputs[2].Value())
	}
	m.dialog.Inputs[1].SetValue(locs[0])
	m.dialog.Inputs[3].SetValue(locs[2])
	m.dialog.Inputs[4].SetValue(testVaultPW)
	cmd, _ = m.confirmDialog()
	run(t, m, cmd)
	if m.dialog.Kind != DialogNone || m.activePane().FS.Kind() != vfs.KindVault || len(m.cfg.SplitVaults) != 1 {
		t.Fatalf("adding it back: dialog %v %q", m.dialog.Kind, m.dialog.Message)
	}
}
