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

	tea "charm.land/bubbletea/v2"

	"shfm/internal/origperm"
	"shfm/internal/vfs"
)

// Emergency permission fixes from the Properties dialog, and their undo:
// every change made there records the entry's previous mode/owner/group
// (only the first time: see package origperm), which Ctrl+R puts back.
// For an entry whose details can't be read (vfs.Entry.StatErr), two more
// ways in: e reads them as root, making the dialog editable; u unlocks the
// folder that hides them, adding just the x bit the user lacks.

// originalsPath is where the originals are kept; TestMain keeps them in
// memory only.
var originalsPath = origperm.DefaultPath

// originals is the store of recorded originals, loaded on first use.
func (m *Model) originals() *origperm.Store {
	if m.origPerms == nil {
		m.origPerms = origperm.Load(originalsPath())
	}
	return m.origPerms
}

// permKey identifies path for the originals store. Local paths stand on
// their own, whichever pane or source they were reached from; anything
// else is qualified by its source.
func permKey(fsys vfs.FileSystem, path string) string {
	if fsys.Kind() == vfs.KindLocal {
		return "local|" + path
	}
	return fsys.Kind().String() + "|" + fsys.Label() + "|" + path
}

// originalOf is what to record of e before changing it. Ownership a
// backend doesn't report is left out, and so never restored.
func originalOf(e vfs.Entry) origperm.Original {
	o := origperm.Original{Mode: e.Mode}
	if e.Owner != "" && e.Group != "" {
		o.Owner, o.Group = e.Owner, e.Group
	}
	return o
}

// describeMode is mode as shown in messages: octal, special bits included.
func describeMode(mode fs.FileMode) string {
	return fmt.Sprintf("%o", vfs.UnixMode(mode))
}

// describeOriginal is o as shown in the dialog, e.g. "766 root:root".
func describeOriginal(o origperm.Original) string {
	s := describeMode(o.Mode)
	if o.Owner != "" {
		s += " " + o.Owner + ":" + o.Group
	}
	return s
}

// propsStatMsg carries the details of PropsPath read as root.
type propsStatMsg struct {
	path  string
	entry vfs.Entry
	err   error
}

// readPropertiesAsRoot (e, on an entry with unreadable details) reads them
// through pkexec, off the event loop: a password prompt may come first.
func (m *Model) readPropertiesAsRoot() tea.Cmd {
	d := m.dialog
	es, ok := d.PropsFS.(vfs.ElevatedStater)
	if !ok || d.PropsEntry.StatErr == nil {
		return nil
	}
	m.setStatus("Reading details as root…")
	path := d.PropsPath
	return func() tea.Msg {
		e, err := es.StatElevated(path)
		return propsStatMsg{path: path, entry: e, err: err}
	}
}

// handlePropsStat shows the details read as root, editable, in the
// Properties dialog still open on that entry.
func (m *Model) handlePropsStat(msg propsStatMsg) {
	if msg.err != nil {
		m.setError("Not read as root: %v", msg.err)
		return
	}
	d := m.dialog
	if d.Kind != DialogProperties || d.PropsPath != msg.path {
		m.setStatus("")
		return
	}
	nd := propertiesDialog(d.PropsFS, d.PropsPath, msg.entry)
	nd.PropsAsRoot = true
	m.dialog = nd
	m.setStatus("")
}

// unlockParentFolder (u, on an entry with unreadable details) adds to the
// folder hiding them the one x bit the user lacks, recording its mode to
// restore later from the folder's own Properties.
func (m *Model) unlockParentFolder() tea.Cmd {
	d := m.dialog
	su, ok1 := d.PropsFS.(vfs.SearchUnlocker)
	pe, ok2 := d.PropsFS.(vfs.PermissionsEditor)
	if !ok1 || !ok2 || !errors.Is(d.PropsEntry.StatErr, fs.ErrPermission) {
		return nil
	}
	dir := d.PropsFS.Dir(d.PropsPath)
	cur, want, ok := su.SearchUnlockMode(dir)
	if !ok {
		m.setError("Folder can't be unlocked")
		return nil
	}
	orig := origperm.Original{Mode: cur}
	if de, err := d.PropsFS.Stat(dir); err == nil {
		orig = originalOf(de)
	}
	m.dialog = Dialog{}
	store, key, name := m.originals(), permKey(d.PropsFS, dir), d.PropsFS.Base(dir)
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

// restoreOriginal (Ctrl+R in Properties) puts back the mode, and owner and
// group when known, the entry had before shfm first changed them.
func (m *Model) restoreOriginal() tea.Cmd {
	d := m.dialog
	pe, ok := d.PropsFS.(vfs.PermissionsEditor)
	if !ok {
		return nil
	}
	store, key := m.originals(), permKey(d.PropsFS, d.PropsPath)
	o, ok := store.Get(key)
	if !ok {
		m.setStatus("No original permissions saved")
		return nil
	}
	uid, gid := -1, -1
	if o.Owner != "" {
		var uerr, gerr error
		uid, uerr = vfs.ResolveUser(o.Owner)
		gid, gerr = vfs.ResolveGroup(o.Group)
		if uerr != nil || gerr != nil {
			m.setError("Unknown owner or group")
			return nil
		}
	}
	m.dialog = Dialog{}
	path := d.PropsPath
	return runElevatable(func() elevatedDoneMsg {
		var err error
		switch oms, both := pe.(vfs.OwnerModeSetter); {
		case uid >= 0 && both:
			err = oms.SetOwnerAndMode(path, uid, gid, o.Mode)
		case uid >= 0:
			err = pe.Chown(path, uid, gid)
			if err == nil {
				err = pe.Chmod(path, o.Mode)
			}
		default:
			err = pe.Chmod(path, o.Mode)
		}
		if err != nil {
			return elevatedDoneMsg{err: fmt.Sprintf("Not restored: %v", err)}
		}
		if err := store.Forget(key); err != nil {
			return elevatedDoneMsg{err: fmt.Sprintf("Restored, record not cleared: %v", err)}
		}
		return elevatedDoneMsg{status: "Restored " + describeOriginal(o)}
	})
}
