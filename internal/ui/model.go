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
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"shfm/internal/config"
	"shfm/internal/semantic"
	"shfm/internal/vfs"
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

	// Background task tracking (copy/move/delete): see tasks.go.
	tasks         []*Task
	nextTaskID    int
	taskCh        chan taskMsg
	sizeCh        chan dirSizeMsg
	connectCh     chan connectResultMsg
	nextConnectID int

	// openCh tracks background downloads of remote entries (SMB/NFS/SFTP/
	// MTP) to a local temp copy before launching an external app on them:
	// see openremote.go.
	openCh     chan openResultMsg
	nextOpenID int

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

	quitting bool
}

type rect struct{ x0, y0, w, h int }

// New creates the initial model, opening both panes on the user's home
// folder (or / on error). keymap resolves keypresses to actions in
// handleKey — see internal/config's KeyMap and config.LoadKeyMap.
func New(cfg *config.Config, keymap *config.KeyMap) *Model {
	start := homeOrRoot()
	local0 := vfs.NewLocalFS("Local", start)
	local1 := vfs.NewLocalFS("Local", start)
	m := &Model{
		cfg:       cfg,
		keymap:    keymap,
		dualPane:  cfg.DualPane,
		taskCh:    make(chan taskMsg, 256),
		sizeCh:    make(chan dirSizeMsg, 256),
		connectCh: make(chan connectResultMsg, 8),
		openCh:    make(chan openResultMsg, 8),
		searchCh:  make(chan searchResultMsg, 8),

		semanticEngine:   semantic.New(),
		semanticCh:       make(chan semanticMsg, 8),
		semanticIndexing: map[string]bool{},
	}
	m.panes[0] = NewPane(local0, start, cfg.ShowHidden, 0, m.sizeCh)
	m.panes[1] = NewPane(local1, start, cfg.ShowHidden, 1, m.sizeCh)
	return m
}

func homeOrRoot() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	return "/"
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.waitForTaskMsg(), m.waitForSizeMsg(), m.waitForConnectMsg(), m.waitForOpenMsg(), m.waitForSearchMsg(), m.waitForSemanticMsg())
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
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case taskMsg:
		m.handleTaskMsg(msg)
		return m, m.waitForTaskMsg()
	case dirSizeMsg:
		m.handleDirSizeMsg(msg)
		return m, m.waitForSizeMsg()
	case connectResultMsg:
		m.handleConnectResult(msg)
		return m, m.waitForConnectMsg()
	case openResultMsg:
		m.handleOpenResult(msg)
		return m, m.waitForOpenMsg()
	case searchResultMsg:
		m.handleSearchResultMsg(msg)
		return m, m.waitForSearchMsg()
	case semanticMsg:
		m.handleSemanticMsg(msg)
		return m, m.waitForSemanticMsg()
	}
	return m, nil
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
		}

	// --- help and layout ---
	case config.ActionHelp:
		m.openHelp()
	case config.ActionToggleLayout:
		m.toggleLayout()
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

// requestQuit quits immediately if no background task is still running;
// otherwise opens a confirmation dialog warning that quitting now would
// abandon them.
func (m *Model) requestQuit() {
	if m.hasRunningTasks() {
		n := 0
		for _, t := range m.tasks {
			if !t.Finished {
				n++
			}
		}
		m.dialog = Dialog{
			Kind:  DialogConfirmQuit,
			Title: "Background tasks are running",
			Message: fmt.Sprintf(
				"%d background task(s) are still running.\nQuitting now will abandon them, possibly leaving\ncopies/moves/deletions incomplete.", n),
		}
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
		m.openWithDefaultApp(e)
	}
}
