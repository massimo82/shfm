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
	"strings"
	"sync"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"shfm/internal/cloud"
	"shfm/internal/config"
	"shfm/internal/vault"
	"shfm/internal/vfs"
)

// Split vaults (see internal/vault's DispersedFS): an encrypted vault
// stored on three folders of three sources — the local disk, saved remote
// sources, cloud accounts — two of which are enough to read it. They are
// saved in the configuration (config.SplitVault) and listed in the source
// picker's Vaults section; opening one connects to its three sources and
// shows the vault in the pane in place of its source (a "standalone"
// vault, see vaultExit). With a source unreachable, it opens read-only.

// splitSource is a source a split vault's part can be on.
type splitSource struct {
	id    string // config.VaultPart.Source
	label string
	dial  func() (vfs.FileSystem, error)
}

func remoteIcon(r config.RemoteSource) string {
	switch r.Kind {
	case "nfs":
		return iconSourceNFS
	case "sftp":
		return iconSourceSFTP
	}
	return iconSourceSMB
}

// splitSources are the sources a part can be on: the local disk, the
// saved remote sources and cloud accounts.
func (m *Model) splitSources() []splitSource {
	out := []splitSource{{id: "local", label: iconSourceLocal + " Local disk", dial: func() (vfs.FileSystem, error) {
		return vfs.NewLocalFS("Local", "/"), nil
	}}}
	for _, r := range m.cfg.RemoteSources {
		label, dial := savedSourceDialer(r)
		out = append(out, splitSource{id: "remote:" + r.Name, label: remoteIcon(r) + " " + r.Name + "  " + label, dial: dial})
	}
	if cloud.Available {
		for _, c := range m.cfg.CloudSources {
			label, dial := cloudSourceDialer(c)
			out = append(out, splitSource{id: "cloud:" + c.Account, label: cloudIcon(c.Provider) + " " + label, dial: dial})
		}
	}
	return out
}

func (m *Model) splitSourceByID(id string) (splitSource, bool) {
	for _, s := range m.splitSources() {
		if s.id == id {
			return s, true
		}
	}
	return splitSource{}, false
}

// splitPart is a part ready to connect: dial is nil for a source that's no
// longer saved.
type splitPart struct {
	src  splitSource
	path string
}

func (m *Model) splitParts(sv config.SplitVault) [3]splitPart {
	var parts [3]splitPart
	for i, p := range sv.Parts {
		src, ok := m.splitSourceByID(p.Source)
		if !ok {
			src = splitSource{id: p.Source, label: p.Source}
		}
		parts[i] = splitPart{src: src, path: p.Path}
	}
	return parts
}

// dialSplit connects to the parts, all at once. With need parts or more
// connected it returns the split storage (the others unavailable) and what
// went wrong with each missing one; otherwise an error.
func dialSplit(name string, parts [3]splitPart, need int) (vfs.FileSystem, []string, error) {
	var vparts [3]vault.Part
	var errs [3]error
	var wg sync.WaitGroup
	for i, p := range parts {
		vparts[i].Dir = p.path
		if p.src.dial == nil {
			errs[i] = errors.New("the source " + p.src.id + " is no longer saved")
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			vparts[i].FS, errs[i] = p.src.dial()
		}()
	}
	wg.Wait()
	var problems []string
	for i, err := range errs {
		if err != nil {
			problems = append(problems, fmt.Sprintf("part %d (%s): %v", i+1, parts[i].src.label, err))
		}
	}
	if 3-len(problems) < need {
		for _, p := range vparts {
			if p.FS != nil {
				p.FS.Close()
			}
		}
		return nil, nil, errors.New(strings.Join(problems, "; "))
	}
	return vault.Split(name, vparts), problems, nil
}

// --- source picker -----------------------------------------------------------

func (m *Model) splitVaultMenuEntries() []sourceMenuEntry {
	if !vault.Available {
		return nil
	}
	var out []sourceMenuEntry
	for _, sv := range m.cfg.SplitVaults {
		var where []string
		for _, p := range m.splitParts(sv) {
			where = append(where, strings.TrimSpace(strings.SplitN(p.src.label, "  ", 2)[0]))
		}
		out = append(out, sourceMenuEntry{
			label: iconSourceVault + " " + sv.Name + "  (split: " + strings.Join(where, " · ") + ")",
			kind:  "split-vault", split: sv,
		})
	}
	return append(out, sourceMenuEntry{label: iconSourceAdd + " New encrypted vault…", kind: "new-vault"})
}

func (m *Model) selectSplitVaultMenuItem(e sourceMenuEntry) {
	switch e.kind {
	case "split-vault":
		m.queueCmd(m.openSplitVault(e.split))
	case "new-vault":
		// In the pane's folder by default, Ctrl+T in the form splitting it;
		// inside a vault, only split (see errVaultInVault).
		if m.inVault() {
			m.askNewSplitVault("")
		} else {
			m.askNewVault()
		}
	case "split-repair":
		m.repairSplitVault()
	}
}

// --- opening -----------------------------------------------------------------

type splitOpenedMsg struct {
	requestID int
	pane      int
	sv        config.SplitVault
	fs        vfs.FileSystem
	problems  []string
	err       error
}

// openSplitVault connects to a saved split vault's sources, for the active
// pane; then, unless it's unlocked already, asks for its password.
func (m *Model) openSplitVault(sv config.SplitVault) tea.Cmd {
	parts := m.splitParts(sv)
	id := m.nextConnectID
	m.nextConnectID++
	pane := m.active
	m.dialog = Dialog{Kind: DialogConnecting, Title: "Connecting", Message: "Connecting to the three parts of " + sv.Name + "…", ConnectRequestID: id}
	return func() tea.Msg {
		fs, problems, err := dialSplit(sv.Name, parts, 2)
		if err == nil && !vault.IsVault(fs, "/") {
			fs.Close()
			err = errors.New("its folders hold no vault")
		}
		return splitOpenedMsg{requestID: id, pane: pane, sv: sv, fs: fs, problems: problems, err: err}
	}
}

func (m *Model) handleSplitOpened(msg splitOpenedMsg) {
	waiting := m.waiting(msg.requestID)
	if msg.err != nil {
		if waiting {
			m.dialog = Dialog{}
		}
		m.setError("Could not open the split vault %s: %v", msg.sv.Name, msg.err)
		return
	}
	if !waiting {
		msg.fs.Close()
		return
	}
	t := vaultTarget{pane: msg.pane, fs: msg.fs, dir: "/", name: msg.sv.Name, standalone: true}
	if s, ok := m.vaults[vaultKey(msg.fs, "/")]; ok {
		m.dialog = Dialog{}
		m.showVault(t, s)
		m.warnSplitParts(t)
		return
	}
	m.askUnlockVault(t, false, "")
}

// warnSplitParts says so when a split vault is open with a part missing.
func (m *Model) warnSplitParts(t vaultTarget) {
	if missing := vault.UnavailableParts(t.fs); len(missing) > 0 {
		var names []string
		for _, i := range missing {
			names = append(names, fmt.Sprintf("part %d", i+1))
		}
		m.setError("%s: %s unreachable, read-only until all parts are back", t.name, strings.Join(names, ", "))
	}
}

// --- creating or adding ------------------------------------------------------

func (m *Model) askNewSplitVault(errText string) {
	if !vault.Available {
		m.dialog = Dialog{}
		m.setError("%v", vault.ErrUnavailable)
		return
	}
	choices := m.splitSources()
	placeholders := []string{"vault name", "folder on part 1", "folder on part 2", "folder on part 3",
		fmt.Sprintf("at least %d characters", minVaultPassword), "the same again (new vault only)"}
	inputs := make([]textinput.Model, len(placeholders))
	for i := range inputs {
		ti := textinput.New()
		ti.SetWidth(40)
		ti.CharLimit = 1024
		ti.Placeholder = placeholders[i]
		if i >= 4 {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '•'
		}
		inputs[i] = ti
	}
	inputs[0].Focus()
	var choice [3]int
	for i := range choice {
		choice[i] = min(i, len(choices)-1)
	}
	m.dialog = Dialog{
		Kind: DialogNewSplitVault, Title: "New encrypted vault, split", Inputs: inputs,
		SplitChoices: choices, SplitChoice: choice, Message: errText, IsError: errText != "",
	}
}

type splitCreatedMsg struct {
	requestID int
	pane      int
	form      Dialog // to show again, with the error
	sv        config.SplitVault
	fs        vfs.FileSystem
	v         *vault.Vault
	key       string // the recovery key of a new vault; "" when added
	err       error
}

func (m *Model) submitNewSplitVault() tea.Cmd {
	d := m.dialog
	fail := func(text string) tea.Cmd {
		m.dialog.IsError, m.dialog.Message = true, text
		return nil
	}
	name := strings.TrimSpace(d.Inputs[0].Value())
	if name == "" {
		return fail("Enter a name for the vault")
	}
	for _, sv := range m.cfg.SplitVaults {
		if sv.Name == name {
			return fail("A split vault named " + name + " is already saved")
		}
	}
	sv := config.SplitVault{Name: name}
	var parts [3]splitPart
	for i := range parts {
		src := d.SplitChoices[d.SplitChoice[i]]
		p := strings.TrimSpace(d.Inputs[1+i].Value())
		if p == "" {
			return fail(fmt.Sprintf("Enter the folder of part %d", i+1))
		}
		for j := range i {
			if sv.Parts[j].Source == src.id && sv.Parts[j].Path == p {
				return fail(fmt.Sprintf("Parts %d and %d are the same folder", j+1, i+1))
			}
		}
		sv.Parts[i] = config.VaultPart{Source: src.id, Path: p}
		parts[i] = splitPart{src: src, path: p}
	}
	pw, again := d.Inputs[4].Value(), d.Inputs[5].Value()
	if pw == "" {
		return fail("Enter the password")
	}
	opts := vault.Options{Names: vault.NamesEncrypted, PostQuantum: d.VaultPQ}
	if d.VaultNamesPlain {
		opts.Names = vault.NamesPlain
	}
	id := m.nextConnectID
	m.nextConnectID++
	pane := m.active
	m.dialog = Dialog{Kind: DialogConnecting, Title: "Split vault", Message: "Connecting to the three parts of " + name + "…", ConnectRequestID: id}
	return func() tea.Msg {
		res := splitCreatedMsg{requestID: id, pane: pane, form: d, sv: sv}
		fs, _, err := dialSplit(name, parts, 3)
		if err != nil {
			res.err = err
			return res
		}
		res.fs = fs
		if vault.IsVault(fs, "/") {
			res.v, res.err = vault.Unlock(fs, "/", pw)
			return res
		}
		switch {
		case len([]rune(pw)) < minVaultPassword:
			res.err = fmt.Errorf("no vault here yet: a new one needs %d+ password characters", minVaultPassword)
		case pw != again:
			res.err = errors.New("no vault in these folders yet: repeat the password to create one")
		}
		if res.err == nil {
			res.err = prepareSplitFolders(parts)
		}
		if res.err == nil {
			res.key, res.err = vault.Create(fs, "/", pw, opts)
		}
		if res.err == nil {
			res.v, res.err = vault.UnlockWithKey(fs, "/", res.key)
		}
		return res
	}
}

// prepareSplitFolders makes sure each part's folder exists, empty: created
// if missing (in an existing folder).
func prepareSplitFolders(parts [3]splitPart) error {
	for i, p := range parts {
		fs, err := p.src.dial()
		if err != nil {
			return err
		}
		err = func() error {
			defer fs.Close()
			e, err := fs.Stat(p.path)
			if err != nil {
				if mkErr := fs.Mkdir(p.path); mkErr != nil {
					return fmt.Errorf("part %d: %w", i+1, mkErr)
				}
				return nil
			}
			if !e.IsDir {
				return fmt.Errorf("part %d: %s isn't a folder", i+1, p.path)
			}
			if entries, err := fs.List(p.path); err != nil {
				return fmt.Errorf("part %d: %w", i+1, err)
			} else if len(entries) > 0 {
				return fmt.Errorf("part %d: %s isn't empty", i+1, p.path)
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *Model) handleSplitCreated(msg splitCreatedMsg) {
	waiting := m.waiting(msg.requestID)
	if msg.err != nil {
		if msg.fs != nil {
			msg.fs.Close()
		}
		if waiting {
			form := msg.form
			form.IsError, form.Message = true, vaultErrorText(msg.err)
			m.dialog = form
		} else {
			m.setError("Split vault %s: %s", msg.sv.Name, vaultErrorText(msg.err))
		}
		return
	}
	m.cfg.SplitVaults = append(m.cfg.SplitVaults, msg.sv)
	if err := m.cfg.Save(); err != nil {
		m.setError("Could not save the configuration: %v", err)
	}
	key := vaultKey(msg.fs, "/")
	if _, ok := m.vaults[key]; !ok {
		m.vaults[key] = &vaultSession{v: msg.v, name: msg.sv.Name}
	}
	m.queueCmd(m.startVaultTick())
	t := vaultTarget{pane: msg.pane, fs: msg.fs, dir: "/", name: msg.sv.Name, standalone: true}
	if !waiting {
		msg.fs.Close()
		m.setStatus("Saved the split vault %s", msg.sv.Name)
		return
	}
	if msg.key != "" {
		m.dialog = Dialog{Kind: DialogVaultRecoveryKey, Title: "Recovery key of " + msg.sv.Name, VaultKey: msg.key, VaultTarget: &t, VaultAfterCreate: true}
		m.setStatus("Created the split vault %s", msg.sv.Name)
		return
	}
	m.dialog = Dialog{}
	m.showVault(t, m.vaults[key])
	m.setStatus("Added the split vault %s", msg.sv.Name)
}

// removeSplitVault forgets a saved split vault: its files stay on its
// parts.
func (m *Model) removeSplitVault(sv config.SplitVault) {
	for i, existing := range m.cfg.SplitVaults {
		if existing.Name == sv.Name {
			m.cfg.SplitVaults = append(m.cfg.SplitVaults[:i], m.cfg.SplitVaults[i+1:]...)
			if err := m.cfg.Save(); err != nil {
				m.setError("Could not save the configuration: %v", err)
				return
			}
			m.setStatus("Forgot the split vault %s", sv.Name)
			return
		}
	}
}

// --- repairing ---------------------------------------------------------------

// repairSplitVault makes the parts of the split vault the pane shows whole
// again, in the background (see vault.RepairDispersed).
func (m *Model) repairSplitVault() {
	t, _, ok := m.currentVault()
	if !ok || !t.standalone {
		return
	}
	if len(vault.UnavailableParts(t.fs)) > 0 {
		m.setError("Repairing %s needs all three parts: reopen it once they're reachable", t.name)
		return
	}
	var st vault.RepairStats
	task := m.startSimpleTask(TaskRepair, t.name, func() error {
		var err error
		st, err = vault.RepairDispersed(t.fs)
		return err
	})
	task.onFinish = func(task *Task) {
		if task.LastError != nil {
			return
		}
		if st.Folders+st.Shards+st.Removed+st.Lost == 0 {
			m.setStatus("%s: the three parts are whole, nothing to repair", t.name)
			return
		}
		msg := fmt.Sprintf("%s repaired: %d shards rebuilt, %d folders created, %d leftovers removed", t.name, st.Shards, st.Folders, st.Removed)
		if st.Lost > 0 {
			m.setError("%s; %d files had a single shard left and are lost", msg, st.Lost)
			return
		}
		m.setStatus("%s", msg)
	}
	m.dialog = Dialog{Kind: DialogProgress, Title: "Repair", TaskID: task.ID}
}

// --- the form ----------------------------------------------------------------

// updateSplitVaultKey handles the split vault form's own keys.
func (m *Model) updateSplitVaultKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	d := &m.dialog
	switch msg.String() {
	case "ctrl+left", "ctrl+right":
		if d.FocusIdx >= 1 && d.FocusIdx <= 3 && len(d.SplitChoices) > 0 {
			i, n := d.FocusIdx-1, len(d.SplitChoices)
			step := 1
			if msg.String() == "ctrl+left" {
				step = n - 1
			}
			d.SplitChoice[i] = (d.SplitChoice[i] + step) % n
		}
		return nil, true
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
	return nil, false
}

func (m *Model) renderNewSplitVault(b *strings.Builder) string {
	d := m.dialog
	marker := func(i int) string {
		if i == d.FocusIdx {
			return "▸ "
		}
		return "  "
	}
	b.WriteString(fmt.Sprintf("%s%-17s %s\n", marker(0), "Name:", d.Inputs[0].View()))
	for i := range 3 {
		src := d.SplitChoices[d.SplitChoice[i]]
		b.WriteString(fmt.Sprintf("  %-17s ◂ %s ▸\n", fmt.Sprintf("Part %d source:", i+1), src.label))
		b.WriteString(fmt.Sprintf("%s%-17s %s\n", marker(1+i), "        folder:", d.Inputs[1+i].View()))
	}
	b.WriteString(fmt.Sprintf("%s%-17s %s\n", marker(4), "Password:", d.Inputs[4].View()))
	b.WriteString(fmt.Sprintf("%s%-17s %s\n", marker(5), "Repeat password:", d.Inputs[5].View()))
	names := "encrypted"
	if d.VaultNamesPlain {
		names = "visible"
	}
	b.WriteString(fmt.Sprintf("\n  New vault: file names %s %s · post-quantum %s %s\n",
		names, styleDim.Render("(Ctrl+N)"), onOff(d.VaultPQ), styleDim.Render("(Ctrl+K)")))
	b.WriteString(fmt.Sprintf("  Stored:    split across three sources %s\n", styleDim.Render("(Ctrl+T: in a folder)")))
	b.WriteString("\n" + styleDim.Render(
		"Every file is split over the three folders: none of them holds a\n"+
			"whole file, and any two are enough to read the vault. Folders that\n"+
			"already hold this vault add it back (only the password is needed);\n"+
			"otherwise they must be empty, or not exist yet.") + "\n")
	if d.IsError && d.Message != "" {
		b.WriteString("\n" + styleErr.Render(d.Message) + "\n")
	}
	b.WriteString("\n" + styleDim.Render("Tab next field · Ctrl+←/→ part source · Enter create/add · Esc cancel"))
	return dialogBox(80).Render(b.String())
}
