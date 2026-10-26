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
	"io/fs"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/vfs"
)

// A folder that can't be listed ("permission denied": it lacks the r or x
// permission for the user) leaves the pane with just an error. From there:
// u unlocks it, adding the r and x bits of the user's class (recorded, so
// Ctrl+R in its Properties restores), e lists it as root without changing
// it, and Esc goes back to the folder the user came from.

// folderUnlock is what the pane knows of a folder it couldn't list.
type folderUnlock struct {
	denied       bool        // listing failed with "permission denied"
	ok           bool        // cur→want would let the user in
	cur, want    os.FileMode //
	parentLocked bool        // the folder can't even be looked at: a parent lacks x
}

// unlockFor is how dir, which the user was denied listing, could be
// unlocked.
func unlockFor(fsys vfs.FileSystem, dir string) folderUnlock {
	u := folderUnlock{denied: true}
	lu, ok := fsys.(vfs.ListUnlocker)
	if !ok {
		return u
	}
	if _, isPE := fsys.(vfs.PermissionsEditor); !isPE {
		return u
	}
	u.cur, u.want, u.ok = lu.ListUnlockMode(dir)
	if !u.ok {
		_, err := fsys.Stat(dir)
		u.parentLocked = errors.Is(err, fs.ErrPermission)
	}
	return u
}

// rootListing is a folder's entries as root saw them.
type rootListing struct {
	fs      vfs.FileSystem
	path    string
	entries []vfs.Entry
}

// showsRootList reports whether Load shows rootList, dropping it once the
// pane has left that folder and its subfolders.
func (p *Pane) showsRootList() bool {
	rl := p.rootList
	if rl == nil {
		return false
	}
	if rl.fs != p.FS || p.Path != rl.path && !strings.HasPrefix(p.Path, strings.TrimSuffix(rl.path, "/")+"/") {
		p.rootList = nil
		return false
	}
	return p.Path == rl.path
}

// rootEntries is the root listing with each entry's details read again
// where the user can (a folder with x but not r), dropping those gone
// since; the rest stay as root saw them.
func (p *Pane) rootEntries() []vfs.Entry {
	entries := make([]vfs.Entry, 0, len(p.rootList.entries))
	for _, e := range p.rootList.entries {
		fresh, err := p.FS.Stat(p.FS.Join(p.Path, e.Name))
		switch {
		case err == nil:
			fresh.Name = e.Name
			entries = append(entries, fresh)
		case errors.Is(err, fs.ErrNotExist):
		default:
			entries = append(entries, e)
		}
	}
	return entries
}

// backTarget is where GoBack goes: the last folder listed fine, when on the
// same source and elsewhere; "" for the parent.
func (p *Pane) backTarget() string {
	if p.lastOK == "" || p.lastOKFS != p.FS || p.lastOK == p.Path {
		return ""
	}
	return p.lastOK
}

// GoBack leaves a folder that couldn't be listed for the one the user came
// from (the parent when that's unknown), the cursor on the folder left
// when it's there.
func (p *Pane) GoBack() bool {
	target := p.backTarget()
	if target == "" {
		return p.GoUp()
	}
	from := p.Path
	p.Path = target
	p.Cursor, p.Offset = 0, 0
	p.DeselectAll()
	p.FilterQuery, p.FilterActive = "", false
	p.Load()
	if p.FS.Dir(from) == p.Path {
		p.moveCursorToName(p.FS.Base(from))
	}
	return true
}

// moveCursorToName puts the cursor on the entry called name, if listed.
func (p *Pane) moveCursorToName(name string) {
	for i, e := range p.Entries {
		if e.Name == name {
			p.Cursor = i
			return
		}
	}
}

// lockedDirHints are the lines shown under the error of a folder that
// couldn't be listed: the ways in, and the way back.
func lockedDirHints(p *Pane) []string {
	var hints []string
	if u := p.Unlock; u.denied {
		switch {
		case u.ok:
			hints = append(hints, fmt.Sprintf("u    unlock it: %s→%s (Ctrl+R in its Properties restores)",
				describeMode(u.cur), describeMode(u.want)))
		case u.parentLocked:
			hints = append(hints, "u    can't unlock it: a parent folder is locked too")
		}
		if _, ok := p.FS.(vfs.ElevatedLister); ok {
			hints = append(hints, "e    list it as root, changing nothing")
		}
	}
	back := "Esc  back"
	if t := p.backTarget(); t != "" {
		back = "Esc  back to " + t
	}
	return append(hints, back+" · Backspace parent folder")
}

// lockedDirKey handles u, e and Esc in a pane showing a listing error.
func (m *Model) lockedDirKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	p := m.activePane()
	if p.Mode != PaneNormal || p.Err == nil {
		return nil, false
	}
	switch msg.String() {
	case "esc":
		if p.FilterActive || m.searchState[m.active].running {
			return nil, false
		}
		p.GoBack()
		return nil, true
	case "u":
		switch {
		case p.Unlock.ok:
			return m.unlockListedFolder(), true
		case p.Unlock.parentLocked:
			m.setError("Can't unlock: a parent folder is locked")
			return nil, true
		}
	case "e":
		if p.Unlock.denied {
			return m.listAsRoot(), true
		}
	}
	return nil, false
}

// unlockListedFolder (u) gives the user the r and x bits of their class on
// the folder the active pane couldn't list.
func (m *Model) unlockListedFolder() tea.Cmd {
	p := m.activePane()
	lu, ok1 := p.FS.(vfs.ListUnlocker)
	pe, ok2 := p.FS.(vfs.PermissionsEditor)
	if !ok1 || !ok2 {
		return nil
	}
	cur, want, ok := lu.ListUnlockMode(p.Path)
	if !ok {
		m.setError("Folder can't be unlocked")
		return nil
	}
	m.setStatus("Unlocking…")
	return m.unlockFolder(p.FS, pe, p.Path, cur, want)
}

// unlockFolder sets dir's mode to want, through pkexec when it isn't the
// user's, first recording cur (and its owner and group, when they can be
// read) for Ctrl+R in its Properties.
func (m *Model) unlockFolder(fsys vfs.FileSystem, pe vfs.PermissionsEditor, dir string, cur, want os.FileMode) tea.Cmd {
	orig := originalOf(vfs.Entry{Mode: cur})
	if de, err := fsys.Stat(dir); err == nil {
		orig = originalOf(de)
	}
	store, key, name := m.originals(), permKey(fsys, dir), fsys.Base(dir)
	return runElevatable(func() elevatedDoneMsg {
		if err := pe.Chmod(dir, want); err != nil {
			return elevatedDoneMsg{err: fmt.Sprintf("Not unlocked: %v", err)}
		}
		if err := store.Remember(key, orig); err != nil {
			return elevatedDoneMsg{err: fmt.Sprintf("Unlocked, original not saved: %v", err)}
		}
		return elevatedDoneMsg{status: fmt.Sprintf("Unlocked %s: %s→%s (Ctrl+R in its Properties restores)",
			name, describeMode(cur), describeMode(want))}
	})
}

// rootListMsg carries a folder's listing read as root.
type rootListMsg struct {
	pane    int
	fs      vfs.FileSystem
	path    string
	entries []vfs.Entry
	err     error
}

// listAsRoot (e, or R while showing a listing read as root) reads the
// active pane's folder through pkexec, off the event loop: a password
// prompt may come first.
func (m *Model) listAsRoot() tea.Cmd {
	p := m.activePane()
	el, ok := p.FS.(vfs.ElevatedLister)
	if !ok {
		return nil
	}
	m.setStatus("Listing as root…")
	idx, fsys, path := m.active, p.FS, p.Path
	return func() tea.Msg {
		entries, err := el.ListElevated(path)
		return rootListMsg{pane: idx, fs: fsys, path: path, entries: entries, err: err}
	}
}

// handleRootList shows the listing read as root, if its pane is still on
// that folder.
func (m *Model) handleRootList(msg rootListMsg) {
	if msg.err != nil {
		m.setError("Not listed as root: %v", msg.err)
		return
	}
	p := m.panes[msg.pane]
	if p.FS != msg.fs || p.Path != msg.path {
		m.setStatus("")
		return
	}
	p.rootList = &rootListing{fs: msg.fs, path: msg.path, entries: msg.entries}
	p.Load()
	m.setStatus("Listed as root: files stay unreadable · R reads again")
}
