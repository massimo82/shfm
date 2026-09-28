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

// Package ui implements shfm's interactive interface with bubbletea:
// single/dual pane, keyboard and mouse navigation (including drag&drop),
// multi-selection, background tasks with progress, and every file
// operation.
package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/config"
	"shfm/internal/fusemount"
	"shfm/internal/semantic"
	"shfm/internal/vfs"
	"shfm/internal/wlclip"
)

type dragState struct {
	active    bool
	moved     bool
	pane      int
	names     []string
	fromDir   string
	startIdx  int
	startX    int
	startY    int
	startTime time.Time
	hoverPane int
	hoverIdx  int
}

type clickMemo struct {
	pane int
	idx  int
	t    time.Time
}

// Model is the application's complete state.
type Model struct {
	panes    [2]*Pane
	active   int
	dualPane bool

	width, height int

	clipboard Clipboard
	cfg       *config.Config
	keymap    *config.KeyMap

	dialog Dialog

	status    string
	statusErr bool

	drag      dragState
	lastClick clickMemo

	sourceMenuEntries []sourceMenuEntry

	// dialogRect is the rectangle (absolute terminal cells) occupied by the
	// current dialog box, computed on every render: used to route mouse
	// clicks to source-menu items or form fields.
	dialogRect rect

	// auth is the pending polkit password request, if any: see auth.go.
	auth *authState

	// Background task tracking (copy/move/delete): see tasks.go.
	tasks         []*Task
	nextTaskID    int
	taskCh        chan taskMsg
	sizeCh        chan dirSizeMsg
	connectCh     chan connectResultMsg
	nextConnectID int
	mirrorCheckID int // last background mirror check started (see doMirrorPaste)

	// openCh tracks the background opening of remote entries (SMB/NFS/
	// SFTP/MTP) with an external app: see openremote.go.
	openCh     chan openResultMsg
	nextOpenID int

	// mounts exposes network sources to external apps through FUSE (see
	// internal/fusemount); nil (e.g. in tests) opens remote entries via a
	// temp copy instead. Set by SetMountManager.
	mounts *fusemount.Manager

	// Background recursive search (see search.go): searchCh delivers
	// results, searchState (indexed by pane) tracks each pane's own
	// in-flight walk, if any, so a bare Esc (no dialog open) cancels the
	// active pane's search specifically — searching both panes at once
	// (start one, switch panes, start another) must not let the second
	// search's Esc/cancel or "latest request" bookkeeping clobber the
	// first, unrelated one.
	searchCh     chan searchResultMsg
	nextSearchID int
	searchState  [2]paneSearchState

	// Semantic (content) search — see internal/semantic and
	// semanticsearch.go. semanticEngine is the (possibly stub, see
	// semantic.Available) engine; semanticIndexing tracks which folder
	// paths currently have a background index build running, so Ctrl+F
	// doesn't start a duplicate one.
	semanticEngine   semantic.Engine
	semanticCh       chan semanticMsg
	semanticIndexing map[string]bool

	// Automatic mirrors (see mirror.go): per-pair runtime state, and the
	// non-local sources running mirrors still use (fsLeases), whose Close
	// is deferred (fsCloseLater) if a pane switches away meanwhile.
	mirrors      map[string]*mirrorRuntime
	fsLeases     map[vfs.FileSystem]int
	fsCloseLater map[vfs.FileSystem]bool

	// Shared system clipboard (see sysclip.go): nil when disabled or
	// unavailable. extClip holds the file URIs another application last
	// copied; useExtClip says they're newer than shfm's own clipboard.
	sysclip    *wlclip.Client
	sysclipCh  chan sysclipFilesMsg
	extClip    []string
	useExtClip bool

	// picker is set when shfm runs as a file chooser: see picker.go.
	picker *pickerState

	quitting bool
}

type rect struct{ x0, y0, w, h int }

// New creates the initial model, opening both panes where start says (see
// Start and resolveStart): the user's home folder (or / on error; see
// homeOrRoot) by default. keymap resolves keypresses to actions in
// handleKey — see internal/config's KeyMap and config.LoadKeyMap.
func New(cfg *config.Config, keymap *config.KeyMap, start Start) *Model {
	dir, names := resolveStart(start)
	local0 := vfs.NewLocalFS("Local", dir)
	local1 := vfs.NewLocalFS("Local", dir)
	m := &Model{
		cfg:       cfg,
		keymap:    keymap,
		dualPane:  cfg.DualPane,
		taskCh:    make(chan taskMsg, 256),
		sizeCh:    make(chan dirSizeMsg, 256),
		connectCh: make(chan connectResultMsg, 8),
		openCh:    make(chan openResultMsg, 8),
		searchCh:  make(chan searchResultMsg, 8),
		sysclipCh: make(chan sysclipFilesMsg, 8),

		semanticEngine:   semantic.New(),
		semanticCh:       make(chan semanticMsg, 8),
		semanticIndexing: map[string]bool{},
	}
	m.panes[0] = NewPane(local0, dir, cfg.ShowHidden, 0, m.sizeCh)
	m.panes[1] = NewPane(local1, dir, cfg.ShowHidden, 1, m.sizeCh)
	if start.Pick != nil {
		m.startPicker(*start.Pick)
	}
	m.reveal(names)
	if start.Properties && len(names) > 0 {
		m.openProperties()
	}
	return m
}

func homeOrRoot() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "/"
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.waitForTaskMsg(), m.waitForSizeMsg(), m.waitForConnectMsg(), m.waitForOpenMsg(), m.waitForSearchMsg(), m.waitForSemanticMsg(), mirrorTick(time.Second)}
	if m.cfg.ShareClipboard {
		cmds = append(cmds, connectSysclip, m.waitForSysclipMsg())
	}
	return tea.Batch(cmds...)
}

func (m *Model) activePane() *Pane   { return m.panes[m.active] }
func (m *Model) inactivePane() *Pane { return m.panes[1-m.active] }

func (m *Model) setStatus(format string, args ...interface{}) {
	m.status = fmt.Sprintf(format, args...)
	m.statusErr = false
}

func (m *Model) setError(format string, args ...interface{}) {
	m.status = fmt.Sprintf(format, args...)
	m.statusErr = true
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.keepAuthOnTop()
	if m.quitting && cmd == nil {
		// A path that decided to quit (e.g. a choice made in file chooser
		// mode with a double click) may only have set the flag.
		return m, tea.Quit
	}
	return m, cmd
}

func (m *Model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Entries revealed at startup (see reveal) were chosen before the
		// list's height was known: scroll them into view now.
		for _, idx := range m.shownPanes() {
			m.panes[idx].fixOffset(m.paneGeom(idx).listH)
		}
		return nil
	case tea.KeyPressMsg:
		_, cmd := m.handleKey(msg)
		return cmd
	case tea.MouseMsg:
		_, cmd := m.handleMouse(msg)
		return cmd
	case taskMsg:
		m.handleTaskMsg(msg)
		return m.waitForTaskMsg()
	case dirSizeMsg:
		m.handleDirSizeMsg(msg)
		return m.waitForSizeMsg()
	case connectResultMsg:
		m.handleConnectResult(msg)
		return m.waitForConnectMsg()
	case openResultMsg:
		m.handleOpenResult(msg)
		return m.waitForOpenMsg()
	case searchResultMsg:
		m.handleSearchResultMsg(msg)
		return m.waitForSearchMsg()
	case semanticMsg:
		m.handleSemanticMsg(msg)
		return m.waitForSemanticMsg()
	case mirrorTickMsg:
		return m.handleMirrorTick()
	case sysclipReadyMsg:
		m.handleSysclipReady(msg)
		return nil
	case sysclipFilesMsg:
		m.handleSysclipFiles(msg)
		return m.waitForSysclipMsg()
	case terminalHandoffMsg:
		return m.handleTerminalHandoff(msg)
	case mirrorCheckMsg:
		m.handleMirrorCheck(msg)
		return nil
	case elevatedDoneMsg:
		m.handleElevatedDone(msg)
		return nil
	case authPromptMsg:
		m.handleAuthPrompt(msg)
		return nil
	case authWithdrawnMsg:
		m.handleAuthWithdrawn(msg)
		return nil
	}
	return nil
}

// handleKey routes a keyboard event: to the active dialog's fields if one
// is open; to the PATH text input if the active pane is in path-editing
// mode (Ctrl+P or a click on the field); otherwise to normal
// navigation/shortcuts, resolved from the pressed key to a config.Action
// via m.keymap (see internal/config's KeyMap) — the actual key each action
// fires on is configurable (keybindings.conf), so this function only ever
// switches on the resolved Action, never on msg.String() directly.
//
// The default shortcut scheme never uses function keys F1-F12 (often
// absent or hard to reach on modern keyboards): every action goes through
// Ctrl combinations (and, for "alternative" variants, Ctrl+Alt) — a user
// customizing keybindings.conf is of course free to use them.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.dialog.Kind != DialogNone {
		cmd, _ := m.updateDialogKey(msg)
		return m, cmd
	}

	if p := m.activePane(); p.PathEditing {
		return m, m.handlePathEditKey(msg)
	}

	action, ok := m.keymap.ActionFor(msg.String())
	if !ok {
		return m, nil
	}

	if m.pickKey(action) {
		return m, nil
	}

	listHeight := m.listHeight()
	p := m.activePane()

	switch action {
	case config.ActionQuit:
		m.requestQuit()
		if m.quitting {
			return m, tea.Quit
		}
		return m, nil

	// --- search / filter ---
	case config.ActionSearch:
		m.openSearch()
	case config.ActionSemanticSearch:
		m.openSemanticSearch()
	case config.ActionCancel:
		switch {
		case m.searchState[m.active].running:
			m.cancelSearch()
		case p.ShowingSearchResults:
			p.ExitSearchResults()
		case p.FilterActive:
			p.ClearFilter()
		case m.picker != nil:
			m.pickCancel()
			return m, tea.Quit
		}

	// --- help and layout ---
	case config.ActionHelp:
		m.openHelp()
	case config.ActionToggleLayout:
		m.toggleLayout()
	case config.ActionToggleHidden:
		m.toggleHidden(listHeight)
	case config.ActionTaskList:
		m.openTaskList()

	// --- cursor / pane navigation ---
	case config.ActionCursorUp:
		p.MoveCursor(-1, listHeight)
	case config.ActionCursorDown:
		p.MoveCursor(1, listHeight)
	case config.ActionPaneLeft:
		if m.dualPane {
			m.active = 0
		}
	case config.ActionPaneRight:
		if m.dualPane {
			m.active = 1
		}
	case config.ActionSwitchPane:
		m.active = 1 - m.active
	case config.ActionPageUp:
		p.MoveCursor(-listHeight, listHeight)
	case config.ActionPageDown:
		p.MoveCursor(listHeight, listHeight)
	case config.ActionGoTop:
		p.Cursor, p.Offset = 0, 0
	case config.ActionGoBottom:
		p.MoveCursor(p.Len(), listHeight)
	case config.ActionActivate:
		m.enterOrOpen()
	case config.ActionGoUp:
		if p.Mode == PaneNormal {
			p.GoUp()
		}

	// --- selection ---
	case config.ActionToggleSelect:
		if p.Mode == PaneNormal {
			p.ToggleSelectCurrent()
			p.MoveCursor(1, listHeight)
		}
	case config.ActionSelectAll:
		if p.Mode == PaneNormal {
			p.SelectAll()
		}
	case config.ActionDeselectAll:
		p.DeselectAll()

	// --- copy / paste / move / delete ---
	case config.ActionCopy:
		m.doCopyToClipboard()
	case config.ActionPaste:
		m.doPaste(true)
	case config.ActionPasteMove:
		m.doPaste(false)
	case config.ActionTrash:
		m.askDelete(true)
	case config.ActionDelete:
		m.askDelete(false)
	case config.ActionMirror:
		return m, m.doMirrorPaste()

	// --- rename / new file / new folder / properties ---
	case config.ActionRename:
		m.askRename()
	case config.ActionNewFolder:
		m.askNewFolder()
	case config.ActionNewFile:
		m.askNewFile()
	case config.ActionProperties:
		m.openProperties()

	// --- source and path ---
	case config.ActionSourceMenu:
		m.openSourceMenu()
	case config.ActionEditPath:
		p.BeginPathEdit()

	// --- trash ---
	case config.ActionToggleTrashView:
		m.toggleTrashView()
	case config.ActionRestoreOrRefresh:
		if p.Mode == PaneTrash {
			m.restoreTrashCurrent()
		} else {
			p.Load()
			m.setStatus("Refreshed")
		}
	case config.ActionEmptyTrash:
		if p.Mode == PaneTrash {
			m.askEmptyTrash()
		}
	}
	return m, nil
}

// handlePathEditKey routes keys to the PATH field's text input while the
// active pane is in path-editing mode.
func (m *Model) handlePathEditKey(msg tea.KeyMsg) tea.Cmd {
	p := m.activePane()
	switch msg.String() {
	case "esc":
		p.CancelPathEdit()
		return nil
	case "enter":
		if err := p.CommitPathEdit(); err != nil {
			m.setError("Invalid path: %v", err)
		} else {
			m.setStatus("Path updated")
		}
		return nil
	}
	var cmd tea.Cmd
	p.PathInput, cmd = p.PathInput.Update(msg)
	return cmd
}

func (m *Model) toggleLayout() {
	m.dualPane = !m.dualPane
	m.cfg.DualPane = m.dualPane
	m.cfg.Save()
}

// toggleHidden shows or hides hidden entries (dotfiles, and lost+found at a
// filesystem's root) in both panes, remembering the choice in the config.
func (m *Model) toggleHidden(listHeight int) {
	m.cfg.ShowHidden = !m.cfg.ShowHidden
	m.cfg.Save()
	for _, p := range m.panes {
		if p != nil {
			p.SetShowHidden(m.cfg.ShowHidden, listHeight)
		}
	}
	if m.cfg.ShowHidden {
		m.setStatus("Showing hidden files")
	} else {
		m.setStatus("Hiding hidden files")
	}
}

// SetMountManager sets the manager of the FUSE mounts through which remote
// entries are opened with external apps (see Model.mounts). The caller
// owns it, and unmounts everything once the program exits.
func (m *Model) SetMountManager(mg *fusemount.Manager) { m.mounts = mg }

// requestQuit quits immediately if no background task is still running and
// no app is still using a file opened through a FUSE mount; otherwise opens
// a confirmation dialog warning about what quitting now would interrupt.
func (m *Model) requestQuit() {
	var warnings []string
	title := "Background tasks are running"
	if m.hasRunningTasks() {
		n := 0
		for _, t := range m.tasks {
			if !t.Finished {
				n++
			}
		}
		warnings = append(warnings, fmt.Sprintf(
			"%d background task(s) are still running.\nQuitting now will abandon them, possibly leaving\ncopies/moves/deletions incomplete.", n))
	}
	if m.mounts != nil && m.mounts.Busy() {
		if len(warnings) == 0 {
			title = "Remote files are still open"
		}
		warnings = append(warnings,
			"An application still has files open from a network\nsource: quitting shfm will cut its access to them.")
	}
	if len(warnings) > 0 {
		m.dialog = Dialog{Kind: DialogConfirmQuit, Title: title, Message: strings.Join(warnings, "\n\n")}
		return
	}
	m.quitting = true
}

func (m *Model) listHeight() int {
	g := m.paneGeom(m.active)
	if g.listH < 1 {
		return 1
	}
	return g.listH
}

func (m *Model) enterOrOpen() {
	p := m.activePane()
	if p.Mode == PaneTrash {
		m.restoreTrashCurrent()
		return
	}
	if p.Activate() {
		return
	}
	if e, ok := p.CurrentEntry(); ok && !e.IsDir {
		if m.picker != nil {
			m.pickActivateFile(e)
			return
		}
		m.openWithDefaultApp(e)
	}
}
