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
	"strconv"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"shfm/internal/config"
	"shfm/internal/drives"
	"shfm/internal/opener"
	"shfm/internal/vfs"
)

type DialogKind int

const (
	DialogNone DialogKind = iota
	DialogRename
	DialogNewFile
	DialogNewFolder
	DialogNewChoice
	DialogConfirmTrash
	DialogConfirmPermanent
	DialogConfirmEmptyTrash
	DialogConnectSMB
	DialogConnectNFS
	DialogConnectSFTP
	DialogHelp
	DialogMessage
	DialogSourceMenu
	DialogChooseApp
	DialogProgress
	DialogTaskList
	DialogConfirmQuit
	DialogProperties
	DialogFormatChoose
	DialogFormatConfirm1
	DialogFormatConfirm2
	DialogConnecting
	DialogSearch
	DialogSemanticSearch
	DialogMirrorConfirm
	DialogMirrorList
	DialogMirrorConfirmDelete
)

// Dialog is the state of any currently active modal.
type Dialog struct {
	Kind DialogKind

	// Generic text input fields (rename/newfile/newfolder use Inputs[0];
	// connect forms use several; properties uses a few).
	Inputs   []textinput.Model
	FocusIdx int
	Title    string
	Message  string
	IsError  bool

	// Generic selectable list (source menu, help, task list, new-item
	// choice, format filesystem choice...).
	Items   []string
	ItemIdx int

	// Context data used by specific dialog kinds.
	TaskID           int // DialogProgress
	ConnectRequestID int // DialogConnecting

	ChooseAppMime   string       // DialogChooseApp
	ChooseAppTarget string       // DialogChooseApp: local path to open (ChooseAppRemote nil)
	ChooseApps      []opener.App // DialogChooseApp: candidates, parallel to Items

	// ChooseAppRemote is set instead of ChooseAppTarget when the entry isn't
	// on a source with a real local path (SMB/NFS/SFTP/MTP): picking an app
	// downloads the entry to a local temp copy first, then launches it.
	ChooseAppRemote *remoteOpenTarget

	PropsFS    vfs.FileSystem // DialogProperties
	PropsAttrs []string       // DialogProperties: restricting chattr flags on the entry (immutable, append-only)
	PropsPath  string
	PropsEntry vfs.Entry

	FormatDevice drives.RemovableDevice // DialogFormatChoose/Confirm1/Confirm2
	FormatFSType drives.FSType          // chosen in DialogFormatChoose, carried forward

	// DialogSearch: Inputs[0] is the query. SearchRegex toggles exact-name
	// vs. Go-regexp matching; SearchRecursive toggles the current-folder
	// live filter vs. a background recursive search. See search.go.
	SearchRegex     bool
	SearchRecursive bool

	MirrorPending []config.MirrorPair // DialogMirrorConfirm: pairs to create (Items: backend choice, rsync first, only between local sources)
	MirrorPairID  string              // DialogMirrorConfirmDelete
}

func newSingleInputDialog(kind DialogKind, title, placeholder, value string) Dialog {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.SetValue(value)
	ti.CharLimit = 255
	ti.SetWidth(40)
	ti.Focus()
	ti.CursorEnd()
	return Dialog{Kind: kind, Title: title, Inputs: []textinput.Model{ti}}
}

func newConnectDialog(kind DialogKind) Dialog {
	var labels []string
	switch kind {
	case DialogConnectSMB:
		labels = []string{"Host", "Share", "Domain", "User", "Password"}
	case DialogConnectNFS:
		labels = []string{"Host", "Export path", "UID", "GID"}
	case DialogConnectSFTP:
		labels = []string{"Host", "Port", "User", "Password", "Remote path"}
	}
	inputs := make([]textinput.Model, len(labels))
	for i, l := range labels {
		ti := textinput.New()
		ti.Placeholder = l
		ti.SetWidth(32)
		ti.CharLimit = 255
		if (kind == DialogConnectSMB && l == "Password") || (kind == DialogConnectSFTP && l == "Password") {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '•'
		}
		inputs[i] = ti
	}
	inputs[0].Focus()
	title := "Connect to an SMB share"
	if kind == DialogConnectNFS {
		title = "Mount an NFS export"
		// NFSv3 has no login: the server trusts whatever UID/GID the client
		// claims (AUTH_SYS). Pre-fill with the local user's own, since that's
		// what's wanted the vast majority of the time — leaving these blank
		// used to silently mean "root" (0/0), which most servers reject
		// outright via root_squash.
		inputs[2].SetValue(strconv.Itoa(os.Getuid()))
		inputs[3].SetValue(strconv.Itoa(os.Getgid()))
	} else if kind == DialogConnectSFTP {
		title = "Connect to an SFTP server"
	}
	return Dialog{Kind: kind, Title: title, Inputs: inputs}
}

// updateDialogKey handles keyboard input while a Dialog is active. Returns
// the tea.Cmd to run (if any); the returned bool is currently unused by
// callers (kept for signature stability) since Dialog state management
// (closing/replacing) is handled directly by m.confirmDialog() and this
// function.
func (m *Model) updateDialogKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	d := &m.dialog
	if d.Kind == DialogSearch {
		return m.updateSearchDialogKey(msg)
	}
	if d.Kind == DialogSemanticSearch {
		return m.updateSemanticSearchDialogKey(msg)
	}
	if d.Kind == DialogMirrorList {
		if cmd, handled := m.updateMirrorListKey(msg); handled {
			return cmd, true
		}
	}
	switch msg.String() {
	case "esc":
		if d.Kind == DialogProgress {
			// Closing the progress view does NOT stop the task: it just
			// stops watching it in the foreground ("send to background").
			m.dialog = Dialog{}
			return nil, true
		}
		m.dialog = Dialog{}
		return nil, true

	case "c":
		if d.Kind == DialogProgress {
			if t := m.taskByID(d.TaskID); t != nil && !t.Finished {
				t.requestCancel()
			}
			return nil, false
		}

	case "j", "k":
		if hasListNav(d.Kind) {
			if msg.String() == "k" {
				d.ItemIdx = (d.ItemIdx - 1 + maxInt(1, len(d.Items))) % maxInt(1, len(d.Items))
			} else {
				d.ItemIdx = (d.ItemIdx + 1) % maxInt(1, len(d.Items))
			}
			return nil, false
		}
		// In dialogs with text fields, j/k are ordinary characters to type.
		if len(d.Inputs) > 0 {
			var cmd tea.Cmd
			d.Inputs[d.FocusIdx], cmd = d.Inputs[d.FocusIdx].Update(msg)
			return cmd, false
		}
		return nil, false

	case "tab", "shift+tab", "down", "up":
		if len(d.Inputs) > 1 {
			d.Inputs[d.FocusIdx].Blur()
			if msg.String() == "shift+tab" || msg.String() == "up" {
				d.FocusIdx = (d.FocusIdx - 1 + len(d.Inputs)) % len(d.Inputs)
			} else {
				d.FocusIdx = (d.FocusIdx + 1) % len(d.Inputs)
			}
			cmd := d.Inputs[d.FocusIdx].Focus()
			return cmd, false
		}
		if hasListNav(d.Kind) {
			if msg.String() == "up" {
				d.ItemIdx = (d.ItemIdx - 1 + maxInt(1, len(d.Items))) % maxInt(1, len(d.Items))
			} else {
				d.ItemIdx = (d.ItemIdx + 1) % maxInt(1, len(d.Items))
			}
		}
		return nil, false

	case "enter":
		return m.confirmDialog()

	case "y", "Y":
		if isYesNoDialog(d.Kind) {
			return m.confirmDialog()
		}
	case "n", "N":
		if isYesNoDialog(d.Kind) {
			m.dialog = Dialog{}
			return nil, true
		}
	}

	if d.Kind == DialogMessage || d.Kind == DialogHelp {
		m.dialog = Dialog{}
		return nil, true
	}
	if len(d.Inputs) > 0 {
		var cmd tea.Cmd
		d.Inputs[d.FocusIdx], cmd = d.Inputs[d.FocusIdx].Update(msg)
		return cmd, false
	}
	return nil, false
}

func hasListNav(k DialogKind) bool {
	switch k {
	case DialogSourceMenu, DialogHelp, DialogNewChoice, DialogTaskList, DialogFormatChoose,
		DialogMirrorConfirm, DialogMirrorList:
		return true
	default:
		return false
	}
}

func isYesNoDialog(k DialogKind) bool {
	switch k {
	case DialogConfirmTrash, DialogConfirmPermanent, DialogConfirmEmptyTrash,
		DialogConfirmQuit, DialogFormatConfirm1, DialogMirrorConfirmDelete:
		return true
	default:
		return false
	}
}
