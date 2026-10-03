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
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"shfm/internal/fileops"
	"shfm/internal/vault"
	"shfm/internal/vfs"
)

// Encrypted vaults (see internal/vault; without the "vault" build tag none
// of this does anything but say so).
//
// A vault is a folder: entering it (Enter, or a double click) asks for its
// password — or its recovery key — and shows its content in clear, in the
// same pane; ".." at its root leaves it, back to the folder holding it. It
// stays unlocked for the session, so entering it again asks nothing, until
// it's locked: from the source picker, with Ctrl+Alt+L (every vault), or
// on its own after the configured minutes without input
// (vault_auto_lock_minutes). Locking leaves the panes showing it and
// unmounts it for other applications.
//
// "New…" (the [+] button) creates a vault in the current folder, then
// shows its recovery key once. The source picker's "Vaults" section
// changes the password of the vault the pane is in, shows its recovery key
// again (after the password), and locks.

// vaultSession is an unlocked vault, keyed in Model.vaults by vaultKey.
type vaultSession struct {
	v    *vault.Vault
	name string
	// fss are the vault FSs handed to panes: what a FUSE mount of the
	// vault was made from, to unmount it on lock.
	fss []vfs.FileSystem
}

// vaultExit is what a pane showing a vault returns to: the backend, and
// the folder holding the vault.
type vaultExit struct {
	FS    vfs.FileSystem
	Dir   string // the vault's folder on FS
	Label string // the pane's SOURCE label on FS
	key   string // the session's, in Model.vaults
	// Standalone: a split vault, opened from the source picker on storage
	// of its own — there's no folder to go back to, ".." isn't offered.
	Standalone bool
}

// vaultTarget is a vault a dialog is about.
type vaultTarget struct {
	pane int
	fs   vfs.FileSystem // the backend
	dir  string         // the vault's folder on fs
	name string
	// standalone: a split vault, fs its storage, opened for this target
	// alone (see vaultExit.Standalone): shown in the pane whatever it
	// shows meanwhile, and closed if it isn't shown in the end.
	standalone bool
}

func vaultKey(fs vfs.FileSystem, dir string) string {
	return fs.Kind().String() + "|" + fs.Label() + "|" + dir
}

// minVaultPassword is the shortest password a new vault accepts.
const minVaultPassword = 8

// enterVault handles Enter on a folder that is a vault, in the active
// pane: showing it if already unlocked, asking for the password if not.
// It reports false for anything else — a plain folder, or a vault in a
// build that can't open them (then entered as a plain folder, with a
// word of explanation).
func (m *Model) enterVault() bool {
	p := m.activePane()
	e, ok := p.CurrentEntry()
	if !ok || !e.IsDir || IsParentEntry(e) || p.Mode != PaneNormal || m.picker != nil || p.ShowingSearchResults {
		return false
	}
	dir := p.FS.Join(p.Path, e.Name)
	if !vault.IsVault(p.FS, dir) {
		return false
	}
	if !vault.Available {
		m.setError("%s is an encrypted vault: %v — showing its encrypted files", e.Name, vault.ErrUnavailable)
		return false
	}
	t := vaultTarget{pane: m.active, fs: p.FS, dir: dir, name: e.Name}
	if s, ok := m.vaults[vaultKey(p.FS, dir)]; ok {
		m.showVault(t, s)
		return true
	}
	m.askUnlockVault(t, false, "")
	return true
}

// showVault shows the unlocked vault s in t's pane, at its root.
func (m *Model) showVault(t vaultTarget, s *vaultSession) {
	p := m.panes[t.pane]
	// A split vault replaces the pane's source, which is closed.
	var old vfs.FileSystem
	if t.standalone {
		old = p.FS
		if p.VaultExit != nil {
			old = p.VaultExit.FS
		}
	}
	fs := s.v.On(t.fs).FS()
	s.fss = append(s.fss, fs)
	label := p.SourceLabel
	if t.standalone {
		label = ""
	}
	p.VaultExit = &vaultExit{FS: t.fs, Dir: t.dir, Label: label, key: vaultKey(t.fs, t.dir), Standalone: t.standalone}
	p.FS, p.Path, p.SourceLabel = fs, "/", iconSourceVault+" "+s.name
	p.Cursor, p.Offset = 0, 0
	p.DeselectAll()
	p.FilterQuery, p.FilterActive = "", false
	p.Load()
	if old != nil && old != t.fs {
		m.closeFSWhenUnused(old)
	}
	m.lastInput = time.Now()
	m.queueCmd(m.startVaultTick())
}

// --- no vault inside a vault ------------------------------------------------

// vaultInside returns the path of an encrypted vault among items — one of
// them, or a folder at any depth inside one — "" if there's none. A vault
// is never stored inside another (vault.ErrVaultInVault).
func vaultInside(items []fileops.Item) string {
	for _, it := range items {
		if p := findVault(it.FS, it.Path, 0); p != "" {
			return p
		}
	}
	return ""
}

// maxVaultSearchDepth stops findVault in pathological trees.
const maxVaultSearchDepth = 64

func findVault(fs vfs.FileSystem, p string, depth int) string {
	if depth > maxVaultSearchDepth {
		return ""
	}
	if vault.IsVault(fs, p) {
		return p
	}
	entries, err := fs.List(p)
	if err != nil {
		return "" // a file, or unreadable: the copy itself will say
	}
	for _, e := range entries {
		if e.IsDir && !e.IsSymlink {
			if found := findVault(fs, fs.Join(p, e.Name), depth+1); found != "" {
				return found
			}
		}
	}
	return ""
}

// topLevelVault is vaultInside for the items themselves only: one Stat
// each, quick enough to refuse at once, before starting a task.
func topLevelVault(items []fileops.Item) string {
	for _, it := range items {
		if vault.IsVault(it.FS, it.Path) {
			return it.Path
		}
	}
	return ""
}

func vaultInVaultError(p string) error {
	return fmt.Errorf("%w: %s is a vault", vault.ErrVaultInVault, p)
}

// refuseVaultInVault ends a transfer task before it starts, when items
// hold a vault, reporting why; nil if they don't.
func refuseVaultInVault(items []fileops.Item, prog *fileops.Progress) *fileops.Result {
	p := vaultInside(items)
	if p == "" {
		return nil
	}
	err := vaultInVaultError(p)
	if prog != nil && prog.OnItem != nil {
		prog.OnItem(len(items), len(items), p, err)
	}
	return &fileops.Result{Errors: []error{err}}
}

// --- unlocking ---------------------------------------------------------------

func (m *Model) askUnlockVault(t vaultTarget, useKey bool, errText string) {
	in := textinput.New()
	in.SetWidth(48)
	in.CharLimit = 4096
	if useKey {
		in.Placeholder = "AGE-SECRET-KEY-…"
	} else {
		in.Placeholder = "password"
		in.EchoMode = textinput.EchoPassword
		in.EchoCharacter = '•'
	}
	in.Focus()
	m.dialog = Dialog{
		Kind: DialogVaultUnlock, Title: "Unlock the vault " + t.name,
		Inputs: []textinput.Model{in}, VaultTarget: &t, VaultUseKey: useKey,
		Message: errText, IsError: errText != "",
	}
}

type vaultUnlockMsg struct {
	requestID int
	target    vaultTarget
	v         *vault.Vault
	useKey    bool
	err       error
}

func (m *Model) submitUnlockVault() tea.Cmd {
	d := m.dialog
	t, useKey := *d.VaultTarget, d.VaultUseKey
	secret := d.Inputs[0].Value()
	if useKey {
		secret = strings.TrimSpace(secret)
	}
	if secret == "" {
		m.dialog.IsError, m.dialog.Message = true, "Enter the password, or Ctrl+R for the recovery key"
		return nil
	}
	id := m.nextConnectID
	m.nextConnectID++
	m.dialog = Dialog{Kind: DialogConnecting, Title: "Unlocking", Message: "Unlocking " + t.name + "…", ConnectRequestID: id}
	return func() tea.Msg {
		var v *vault.Vault
		var err error
		if useKey {
			v, err = vault.UnlockWithKey(t.fs, t.dir, secret)
		} else {
			v, err = vault.Unlock(t.fs, t.dir, secret)
		}
		return vaultUnlockMsg{requestID: id, target: t, v: v, useKey: useKey, err: err}
	}
}

// waiting reports whether the "Connecting"-style dialog for requestID is
// still shown (not dismissed with Esc).
func (m *Model) waiting(requestID int) bool {
	return m.dialog.Kind == DialogConnecting && m.dialog.ConnectRequestID == requestID
}

func (m *Model) handleVaultUnlock(msg vaultUnlockMsg) tea.Cmd {
	t := msg.target
	if msg.err != nil {
		if m.waiting(msg.requestID) {
			m.askUnlockVault(t, msg.useKey, vaultErrorText(msg.err))
		} else {
			m.setError("Could not unlock %s: %s", t.name, vaultErrorText(msg.err))
		}
		return nil
	}
	key := vaultKey(t.fs, t.dir)
	s, ok := m.vaults[key]
	if !ok {
		s = &vaultSession{v: msg.v, name: t.name}
		m.vaults[key] = s
	}
	if !m.waiting(msg.requestID) {
		if t.standalone {
			t.fs.Close() // not shown: nothing uses it
		}
		m.setStatus("Unlocked the vault %s", t.name)
		return m.startVaultTick()
	}
	m.dialog = Dialog{}
	m.setStatus("Unlocked the vault %s", t.name)
	if p := m.panes[t.pane]; t.standalone || (p.FS == t.fs && p.VaultExit == nil) {
		m.showVault(t, s)
		m.warnSplitParts(t)
	}
	return m.startVaultTick()
}

func vaultErrorText(err error) string {
	switch {
	case errors.Is(err, vault.ErrWrongPassword):
		return "Wrong password"
	case errors.Is(err, vault.ErrWrongKey):
		return "This recovery key doesn't open this vault"
	}
	return err.Error()
}

// --- creating ----------------------------------------------------------------

// errVaultInVault: a vault is never created inside another.
const errVaultInVault = "A vault can't be created inside another vault"

// inVault reports whether the active pane shows a vault's content.
func (m *Model) inVault() bool { return m.activePane().FS.Kind() == vfs.KindVault }

func (m *Model) askNewVault() {
	p := m.activePane()
	if p.Mode != PaneNormal {
		m.setError("Open a folder first: a new vault is created in the pane's folder")
		return
	}
	if m.inVault() {
		m.dialog = Dialog{}
		m.setError("%s", errVaultInVault)
		return
	}
	if !vault.Available {
		m.dialog = Dialog{}
		m.setError("%v", vault.ErrUnavailable)
		return
	}
	inputs := make([]textinput.Model, 3)
	for i := range inputs {
		ti := textinput.New()
		ti.SetWidth(36)
		ti.CharLimit = 255
		inputs[i] = ti
	}
	inputs[0].Placeholder = "folder name"
	inputs[1].Placeholder = fmt.Sprintf("at least %d characters", minVaultPassword)
	inputs[1].CharLimit = 1024
	inputs[2].Placeholder = "the same again"
	inputs[2].CharLimit = 1024
	for _, i := range []int{1, 2} {
		inputs[i].EchoMode = textinput.EchoPassword
		inputs[i].EchoCharacter = '•'
	}
	inputs[0].Focus()
	m.dialog = Dialog{Kind: DialogNewVault, Title: "New encrypted vault", Inputs: inputs}
}

// switchVaultForm goes from the new vault form to the split vault one, or
// back, keeping what was typed and chosen: a vault in the current folder
// is the default, the simpler.
func (m *Model) switchVaultForm() {
	d := m.dialog
	var name, pw, again string
	if d.Kind == DialogNewVault {
		name, pw, again = d.Inputs[0].Value(), d.Inputs[1].Value(), d.Inputs[2].Value()
		m.askNewSplitVault("")
		typed := []string{name, "", "", "", pw, again}
		for i, v := range typed {
			m.dialog.Inputs[i].SetValue(v)
		}
	} else {
		name, pw, again = d.Inputs[0].Value(), d.Inputs[4].Value(), d.Inputs[5].Value()
		if m.activePane().Mode != PaneNormal {
			return
		}
		if m.inVault() {
			// A split vault replaces the pane's source: the only kind
			// that can be made from inside a vault.
			m.dialog.IsError, m.dialog.Message = true, errVaultInVault+": only a split vault, which opens in place of this one"
			return
		}
		m.askNewVault()
		for i, v := range []string{name, pw, again} {
			m.dialog.Inputs[i].SetValue(v)
		}
	}
	m.dialog.VaultNamesPlain, m.dialog.VaultPQ = d.VaultNamesPlain, d.VaultPQ
}

type vaultCreatedMsg struct {
	requestID int
	target    vaultTarget
	v         *vault.Vault
	key       string
	err       error
}

func (m *Model) submitNewVault() tea.Cmd {
	d := m.dialog
	p := m.activePane()
	name := strings.TrimSpace(d.Inputs[0].Value())
	pw, again := d.Inputs[1].Value(), d.Inputs[2].Value()
	fail := func(text string) tea.Cmd {
		m.dialog.IsError, m.dialog.Message = true, text
		return nil
	}
	switch {
	case name == "" || strings.ContainsRune(name, '/') || name == "." || name == "..":
		return fail("Enter a valid folder name")
	case len([]rune(pw)) < minVaultPassword:
		return fail(fmt.Sprintf("The password needs at least %d characters", minVaultPassword))
	case pw != again:
		return fail("The two passwords differ")
	}
	if p.FS.Kind() == vfs.KindVault {
		return fail(errVaultInVault)
	}
	dir := p.FS.Join(p.Path, name)
	if _, err := p.FS.Stat(dir); err == nil {
		return fail(name + " already exists here")
	}
	opts := vault.Options{Names: vault.NamesEncrypted, PostQuantum: d.VaultPQ}
	if d.VaultNamesPlain {
		opts.Names = vault.NamesPlain
	}
	t := vaultTarget{pane: m.active, fs: p.FS, dir: dir, name: name}
	id := m.nextConnectID
	m.nextConnectID++
	m.dialog = Dialog{Kind: DialogConnecting, Title: "Creating the vault", Message: "Creating the vault " + name + "…", ConnectRequestID: id}
	return func() tea.Msg {
		if err := t.fs.Mkdir(t.dir); err != nil {
			return vaultCreatedMsg{requestID: id, target: t, err: err}
		}
		key, err := vault.Create(t.fs, t.dir, pw, opts)
		var v *vault.Vault
		if err == nil {
			v, err = vault.UnlockWithKey(t.fs, t.dir, key)
		}
		if err != nil {
			t.fs.Remove(t.dir) // the folder made just above, for this vault only
		}
		return vaultCreatedMsg{requestID: id, target: t, v: v, key: key, err: err}
	}
}

func (m *Model) handleVaultCreated(msg vaultCreatedMsg) tea.Cmd {
	t := msg.target
	m.panes[t.pane].Load()
	if msg.err != nil {
		if m.waiting(msg.requestID) {
			m.dialog = Dialog{}
		}
		m.setError("Could not create the vault %s: %v", t.name, msg.err)
		return nil
	}
	m.vaults[vaultKey(t.fs, t.dir)] = &vaultSession{v: msg.v, name: t.name}
	m.dialog = Dialog{Kind: DialogVaultRecoveryKey, Title: "Recovery key of " + t.name, VaultKey: msg.key, VaultTarget: &t, VaultAfterCreate: true}
	m.setStatus("Created the vault %s", t.name)
	return m.startVaultTick()
}

// closeRecoveryKey closes the recovery key: after creating a vault, the
// pane then shows it.
func (m *Model) closeRecoveryKey() {
	d := m.dialog
	m.dialog = Dialog{}
	if !d.VaultAfterCreate || d.VaultTarget == nil {
		return
	}
	t := *d.VaultTarget
	s, ok := m.vaults[vaultKey(t.fs, t.dir)]
	if p := m.panes[t.pane]; ok && (t.standalone || (p.FS == t.fs && p.VaultExit == nil)) {
		m.showVault(t, s)
	}
}

// --- password and recovery key -----------------------------------------------

// currentVault is the vault the active pane is in, if any.
func (m *Model) currentVault() (vaultTarget, *vaultSession, bool) {
	p := m.activePane()
	if p.VaultExit == nil {
		return vaultTarget{}, nil, false
	}
	s, ok := m.vaults[p.VaultExit.key]
	if !ok {
		return vaultTarget{}, nil, false
	}
	return vaultTarget{pane: m.active, fs: p.VaultExit.FS, dir: p.VaultExit.Dir, name: s.name, standalone: p.VaultExit.Standalone}, s, true
}

func (m *Model) askVaultPassword() {
	t, _, ok := m.currentVault()
	if !ok {
		return
	}
	labels := []string{"current password", fmt.Sprintf("at least %d characters", minVaultPassword), "the same again"}
	inputs := make([]textinput.Model, 3)
	for i := range inputs {
		ti := textinput.New()
		ti.SetWidth(36)
		ti.CharLimit = 1024
		ti.Placeholder = labels[i]
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
		inputs[i] = ti
	}
	inputs[0].Focus()
	m.dialog = Dialog{Kind: DialogVaultPassword, Title: "Change the password of " + t.name, Inputs: inputs, VaultTarget: &t}
}

type vaultPasswordMsg struct {
	requestID int
	target    vaultTarget
	err       error
}

func (m *Model) submitVaultPassword() tea.Cmd {
	d := m.dialog
	t := *d.VaultTarget
	cur, pw, again := d.Inputs[0].Value(), d.Inputs[1].Value(), d.Inputs[2].Value()
	switch {
	case cur == "":
		m.dialog.IsError, m.dialog.Message = true, "Enter the current password"
		return nil
	case len([]rune(pw)) < minVaultPassword:
		m.dialog.IsError, m.dialog.Message = true, fmt.Sprintf("The new password needs at least %d characters", minVaultPassword)
		return nil
	case pw != again:
		m.dialog.IsError, m.dialog.Message = true, "The two new passwords differ"
		return nil
	}
	id := m.nextConnectID
	m.nextConnectID++
	m.dialog = Dialog{Kind: DialogConnecting, Title: "Changing the password", Message: "Changing the password of " + t.name + "…", ConnectRequestID: id}
	return func() tea.Msg {
		// The current password is checked by opening the vault with it.
		v, err := vault.Unlock(t.fs, t.dir, cur)
		if err == nil {
			err = v.ChangePassword(pw)
		}
		return vaultPasswordMsg{requestID: id, target: t, err: err}
	}
}

func (m *Model) handleVaultPassword(msg vaultPasswordMsg) {
	if m.waiting(msg.requestID) {
		m.dialog = Dialog{}
	}
	if msg.err != nil {
		m.setError("The password of %s was not changed: %s", msg.target.name, vaultErrorText(msg.err))
		return
	}
	m.setStatus("Changed the password of %s", msg.target.name)
}

func (m *Model) askVaultShowKey() {
	t, _, ok := m.currentVault()
	if !ok {
		return
	}
	in := textinput.New()
	in.SetWidth(36)
	in.CharLimit = 1024
	in.Placeholder = "password"
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '•'
	in.Focus()
	m.dialog = Dialog{Kind: DialogVaultShowKey, Title: "Recovery key of " + t.name, Inputs: []textinput.Model{in}, VaultTarget: &t}
}

type vaultShowKeyMsg struct {
	requestID int
	target    vaultTarget
	key       string
	err       error
}

func (m *Model) submitVaultShowKey() tea.Cmd {
	d := m.dialog
	t := *d.VaultTarget
	pw := d.Inputs[0].Value()
	if pw == "" {
		m.dialog.IsError, m.dialog.Message = true, "Enter the password"
		return nil
	}
	id := m.nextConnectID
	m.nextConnectID++
	m.dialog = Dialog{Kind: DialogConnecting, Title: "Checking the password", Message: "Checking the password of " + t.name + "…", ConnectRequestID: id}
	return func() tea.Msg {
		v, err := vault.Unlock(t.fs, t.dir, pw)
		var key string
		if err == nil {
			key, err = v.RecoveryKey()
			v.Lock()
		}
		return vaultShowKeyMsg{requestID: id, target: t, key: key, err: err}
	}
}

func (m *Model) handleVaultShowKey(msg vaultShowKeyMsg) {
	if !m.waiting(msg.requestID) {
		return // dismissed: the key isn't shown unasked
	}
	if msg.err != nil {
		m.dialog = Dialog{}
		m.setError("%s", vaultErrorText(msg.err))
		return
	}
	t := msg.target
	m.dialog = Dialog{Kind: DialogVaultRecoveryKey, Title: "Recovery key of " + t.name, VaultKey: msg.key, VaultTarget: &t}
}

// --- locking -----------------------------------------------------------------

// lockVault locks the vault keyed key: the panes showing it leave it, its
// FUSE mount goes, and shfm's clipboard forgets entries copied from it.
func (m *Model) lockVault(key string) {
	s, ok := m.vaults[key]
	if !ok {
		return
	}
	s.v.Lock()
	delete(m.vaults, key)
	for i, p := range m.panes {
		switch {
		case p.VaultExit == nil || p.VaultExit.key != key:
		case p.VaultExit.Standalone:
			home := homeOrRoot()
			m.replaceFS(i, vfs.NewLocalFS("Local", home), home)
		default:
			p.leaveVault()
		}
	}
	for _, fs := range s.fss {
		if m.clipboard.FS == fs {
			m.clipboard = Clipboard{}
		}
	}
	if m.mounts != nil {
		mounts, fss := m.mounts, s.fss
		go func() {
			for _, fs := range fss {
				mounts.Unmount(fs)
			}
		}()
	}
}

func (m *Model) lockAllVaults() int {
	n := len(m.vaults)
	for key := range m.vaults {
		m.lockVault(key)
	}
	return n
}

// lockVaultsAction is Ctrl+Alt+L.
func (m *Model) lockVaultsAction() {
	switch n := m.lockAllVaults(); n {
	case 0:
		m.setStatus("No vault is unlocked")
	case 1:
		m.setStatus("Locked the vault")
	default:
		m.setStatus("Locked %d vaults", n)
	}
}

type vaultTickMsg struct{}

// vaultTickEvery is how often the auto-lock checks for inactivity.
const vaultTickEvery = 30 * time.Second

// startVaultTick starts the auto-lock's periodic check, if not running.
func (m *Model) startVaultTick() tea.Cmd {
	if m.vaultTicking || len(m.vaults) == 0 {
		return nil
	}
	m.vaultTicking = true
	return tea.Tick(vaultTickEvery, func(time.Time) tea.Msg { return vaultTickMsg{} })
}

// handleVaultTick locks every vault once shfm has had no input for the
// configured time — unless a task is still running, which may be working
// on a vault.
func (m *Model) handleVaultTick() tea.Cmd {
	m.vaultTicking = false
	if len(m.vaults) == 0 {
		return nil
	}
	limit := time.Duration(m.cfg.VaultAutoLockMinutes) * time.Minute
	if limit > 0 && time.Since(m.lastInput) >= limit && !m.tasksRunning() {
		m.lockAllVaults()
		m.setStatus("Vaults locked after %d minutes without activity", m.cfg.VaultAutoLockMinutes)
		return nil
	}
	return m.startVaultTick()
}

func (m *Model) tasksRunning() bool {
	for _, t := range m.tasks {
		if !t.Finished {
			return true
		}
	}
	return false
}

// --- source picker -----------------------------------------------------------

// vaultMenuEntries are the source picker's "Vaults" section: the vault the
// pane is in, then locking each unlocked one.
func (m *Model) vaultMenuEntries() []sourceMenuEntry {
	var entries []sourceMenuEntry
	if t, _, ok := m.currentVault(); ok {
		entries = append(entries,
			sourceMenuEntry{label: iconSourceVault + " Change the password of " + t.name + "…", kind: "vault-password"},
			sourceMenuEntry{label: iconSourceVault + " Show the recovery key of " + t.name + "…", kind: "vault-key"},
		)
		if t.standalone {
			entries = append(entries, sourceMenuEntry{label: iconSourceVault + " Repair the parts of " + t.name + "…", kind: "split-repair"})
		}
	}
	keys := make([]string, 0, len(m.vaults))
	for k := range m.vaults {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m.vaults[keys[i]].name < m.vaults[keys[j]].name })
	for _, k := range keys {
		entries = append(entries, sourceMenuEntry{label: iconSourceVault + " Lock " + m.vaults[k].name, kind: "vault-lock", vaultKey: k})
	}
	if len(keys) > 1 {
		entries = append(entries, sourceMenuEntry{label: iconSourceVault + " Lock all vaults", kind: "vault-lock-all"})
	}
	return append(entries, m.splitVaultMenuEntries()...)
}

func (m *Model) selectVaultMenuItem(e sourceMenuEntry) {
	m.dialog = Dialog{}
	switch e.kind {
	case "vault-password":
		m.askVaultPassword()
	case "vault-key":
		m.askVaultShowKey()
	case "vault-lock":
		name := m.vaults[e.vaultKey].name
		m.lockVault(e.vaultKey)
		m.setStatus("Locked the vault %s", name)
	case "vault-lock-all":
		m.lockVaultsAction()
	default:
		m.selectSplitVaultMenuItem(e)
	}
}

// --- dialogs -----------------------------------------------------------------

func isVaultDialog(k DialogKind) bool {
	switch k {
	case DialogVaultUnlock, DialogNewVault, DialogVaultRecoveryKey, DialogVaultPassword, DialogVaultShowKey, DialogNewSplitVault:
		return true
	}
	return false
}

// updateVaultDialogKey handles the vault dialogs' own keys; the rest (Tab,
// Enter, typing) goes through updateDialogKey's usual handling.
func (m *Model) updateVaultDialogKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	d := &m.dialog
	switch d.Kind {
	case DialogVaultUnlock:
		switch msg.String() {
		case "ctrl+r":
			m.askUnlockVault(*d.VaultTarget, !d.VaultUseKey, "")
			return nil, true
		case "esc":
			if d.VaultTarget.standalone {
				d.VaultTarget.fs.Close() // opened for this unlock only
			}
			m.dialog = Dialog{}
			return nil, true
		}
	case DialogNewVault:
		switch msg.String() {
		case "ctrl+n":
			d.VaultNamesPlain = !d.VaultNamesPlain
			return nil, true
		case "ctrl+k":
			d.VaultPQ = !d.VaultPQ
			return nil, true
		case "ctrl+t":
			m.switchVaultForm()
			return nil, true
		}
	case DialogNewSplitVault:
		return m.updateSplitVaultKey(msg)
	case DialogVaultRecoveryKey:
		// Every key closes it, Enter as Esc: there's nothing to cancel.
		m.closeRecoveryKey()
		return nil, true
	}
	return nil, false
}

// passwordStrength rates a password roughly, for the new vault form: by
// length and variety of characters, not a dictionary check.
func passwordStrength(pw string) string {
	n := len([]rune(pw))
	if n == 0 {
		return ""
	}
	classes := 0
	var lower, upper, digit, other bool
	for _, r := range pw {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			classes++
		}
	}
	switch {
	case n < minVaultPassword:
		return styleErr.Render("too short")
	case n >= 20 || (n >= 14 && classes >= 3):
		return styleAccent.Render("strong")
	case n >= 12 || classes >= 3:
		return styleDim.Render("fair")
	default:
		return styleErr.Render("weak")
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (m *Model) renderVaultDialog(b *strings.Builder) string {
	d := m.dialog
	field := func(i int, label string) {
		marker := "  "
		if i == d.FocusIdx {
			marker = "▸ "
		}
		b.WriteString(fmt.Sprintf("%s%-18s %s\n", marker, label+":", d.Inputs[i].View()))
	}
	errLine := func() {
		if d.IsError && d.Message != "" {
			b.WriteString("\n" + styleErr.Render(d.Message) + "\n")
		}
	}
	switch d.Kind {
	case DialogVaultUnlock:
		if d.VaultUseKey {
			field(0, "Recovery key")
		} else {
			field(0, "Password")
		}
		errLine()
		toggle := "Ctrl+R use the recovery key"
		if d.VaultUseKey {
			toggle = "Ctrl+R use the password"
		}
		b.WriteString("\n" + styleDim.Render("Enter unlock · "+toggle+" · Esc cancel"))
		return dialogBox(72).Render(b.String())

	case DialogNewVault:
		field(0, "Name")
		field(1, "Password")
		field(2, "Repeat password")
		b.WriteString(fmt.Sprintf("  %-18s %s\n", "Strength:", passwordStrength(d.Inputs[1].Value())))
		b.WriteString("\n")
		names := "encrypted"
		if d.VaultNamesPlain {
			names = "visible (name.age files, plain folders)"
		}
		b.WriteString(fmt.Sprintf("  File names:     %s  %s\n", names, styleDim.Render("(Ctrl+N)")))
		b.WriteString(fmt.Sprintf("  Post-quantum:   %s  %s\n", onOff(d.VaultPQ), styleDim.Render("(Ctrl+K)")))
		b.WriteString(fmt.Sprintf("  Stored:         in this folder  %s\n", styleDim.Render("(Ctrl+T: split across three sources)")))
		if d.VaultPQ {
			b.WriteString(styleDim.Render("  needs age 1.3+ (or age-plugin-pq) to recover without shfm") + "\n")
		}
		b.WriteString("\n" + styleDim.Render("Every file is a standard age file: see RECOVERY.txt in the vault.\nIf the password and the recovery key are both lost, so are the files.") + "\n")
		errLine()
		b.WriteString("\n" + styleDim.Render("Tab next field · Enter create · Esc cancel"))
		return dialogBox(72).Render(b.String())

	case DialogVaultPassword:
		field(0, "Current password")
		field(1, "New password")
		field(2, "Repeat password")
		b.WriteString(fmt.Sprintf("  %-18s %s\n", "Strength:", passwordStrength(d.Inputs[1].Value())))
		b.WriteString("\n" + styleDim.Render("Only the vault's key file is re-encrypted: the files are untouched,\nand the old password stops working at once.") + "\n")
		errLine()
		b.WriteString("\n" + styleDim.Render("Tab next field · Enter change · Esc cancel"))
		return dialogBox(72).Render(b.String())

	case DialogVaultShowKey:
		field(0, "Password")
		errLine()
		b.WriteString("\n" + styleDim.Render("Enter show the key · Esc cancel"))
		return dialogBox(72).Render(b.String())

	case DialogVaultRecoveryKey:
		// The box's content is w-4 wide (padding), each key line indented by 2.
		w := min(max(len(d.VaultKey)+6, 72), max(m.width-8, 40))
		b.WriteString("The recovery key opens the vault without its password, and decrypts\n" +
			"its files with the age tools (see RECOVERY.txt in the vault).\n\n")
		for key := d.VaultKey; key != ""; {
			n := min(len(key), w-6)
			b.WriteString("  " + styleAccent.Render(key[:n]) + "\n")
			key = key[n:]
		}
		b.WriteString("\nKeep it somewhere safe, away from the vault (on paper, in a password\n" +
			"manager). If both the password and this key are lost, nobody can\n" +
			"recover the files.\n")
		if len(d.VaultKey) > w-6 {
			b.WriteString(styleDim.Render("(one key on several lines: join them, without spaces)") + "\n")
		}
		b.WriteString("\n" + styleDim.Render("Any key: I have saved it"))
		return dialogBox(w).Render(b.String())
	}
	return dialogBox(64).Render(b.String())
}
