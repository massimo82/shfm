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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"shfm/internal/config"
	"shfm/internal/drives"
	"shfm/internal/fileops"
	"shfm/internal/mirror"
	"shfm/internal/vfs"
)

// Automatic one-way mirrors (see package mirror): Ctrl+Alt+M pastes the
// clipboard as a mirror — after confirmation the pair is saved in the
// config and synced right away, then again every mirrorInterval for as
// long as shfm is running and both ends are available. A local disk or
// removable drive is available while mounted (recognized by filesystem
// UUID, wherever it gets mounted); a network or MTP source while one of
// the panes has it open.
const (
	mirrorInterval  = 5 * time.Minute
	mirrorPollEvery = 30 * time.Second
)

// mirrorRuntime is the in-memory state of one saved pair.
type mirrorRuntime struct {
	running   bool
	available bool // both ends available at the last check
	lastRun   time.Time
	lastErr   string
	taskID    int
	hasTask   bool
}

type mirrorTickMsg struct{}

func mirrorTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return mirrorTickMsg{} })
}

func (m *Model) handleMirrorTick() tea.Cmd {
	m.checkMirrors()
	return mirrorTick(mirrorPollEvery)
}

func (m *Model) mirrorState(id string) *mirrorRuntime {
	if m.mirrors == nil {
		m.mirrors = map[string]*mirrorRuntime{}
	}
	rt := m.mirrors[id]
	if rt == nil {
		rt = &mirrorRuntime{}
		m.mirrors[id] = rt
	}
	return rt
}

// checkMirrors starts every pair that is due: both ends available and
// either just became available or last synced mirrorInterval ago.
func (m *Model) checkMirrors() {
	if m.cfg == nil {
		return
	}
	now := time.Now()
	for _, pair := range m.cfg.MirrorPairs {
		rt := m.mirrorState(pair.ID)
		if pair.Paused {
			rt.available = false
			continue
		}
		src, okS := m.resolveEndpoint(pair.Src)
		dst, okD := m.resolveEndpoint(pair.Dst)
		wasAvailable := rt.available
		rt.available = okS && okD
		if !rt.available || rt.running {
			continue
		}
		if !wasAvailable || now.Sub(rt.lastRun) >= mirrorInterval {
			m.startMirror(pair, src, dst, false)
		}
	}
}

// endpointFor builds the stable identity of path on fs.
func endpointFor(fs vfs.FileSystem, path string) config.MirrorEndpoint {
	if fs.Kind() != vfs.KindLocal {
		return config.MirrorEndpoint{Source: fs.Label(), Path: path, Label: fs.Label() + path}
	}
	ep := config.MirrorEndpoint{Source: "local", Path: path, Label: path}
	if mt, ok := drives.MountOf(path); ok && mt.UUID != "" {
		if rel, err := filepath.Rel(mt.MountPoint, path); err == nil {
			if rel == "." {
				rel = ""
			}
			ep.Source, ep.Path = "uuid:"+mt.UUID, rel
		}
	}
	return ep
}

// resolveEndpoint finds ep's source if it's available right now.
func (m *Model) resolveEndpoint(ep config.MirrorEndpoint) (mirror.Side, bool) {
	switch {
	case strings.HasPrefix(ep.Source, "uuid:"):
		mt, ok := drives.MountByUUID(strings.TrimPrefix(ep.Source, "uuid:"))
		if !ok {
			return mirror.Side{}, false
		}
		return mirror.Side{FS: vfs.NewLocalFS(mt.MountPoint, mt.MountPoint), Path: filepath.Join(mt.MountPoint, ep.Path), FSType: mt.FSType}, true
	case ep.Source == "local":
		side := mirror.Side{FS: vfs.NewLocalFS("Local", "/"), Path: ep.Path}
		if mt, ok := drives.MountOf(ep.Path); ok {
			side.FSType = mt.FSType
		}
		return side, true
	}
	for _, p := range m.panes {
		if p.FS.Kind() != vfs.KindLocal && p.FS.Label() == ep.Source {
			return mirror.Side{FS: p.FS, Path: ep.Path}, true
		}
	}
	return mirror.Side{}, false
}

// displayEndpoint shows where ep is right now, or how it looked when the
// pair was created if its source isn't available.
func (m *Model) displayEndpoint(ep config.MirrorEndpoint) string {
	if strings.HasPrefix(ep.Source, "uuid:") {
		if side, ok := m.resolveEndpoint(ep); ok {
			return side.Path
		}
	}
	return ep.Label
}

func (m *Model) mirrorLabel(pair config.MirrorPair) string {
	return m.displayEndpoint(pair.Src) + " → " + m.displayEndpoint(pair.Dst)
}

// startMirror runs one sync of pair as a background task; foreground
// opens its progress dialog.
func (m *Model) startMirror(pair config.MirrorPair, src, dst mirror.Side, foreground bool) {
	rt := m.mirrorState(pair.ID)
	if rt.running {
		return
	}
	// Keep the task list from growing by one entry per pair every few
	// minutes: drop this pair's previous, finished run.
	if rt.hasTask {
		if old := m.taskByID(rt.taskID); old != nil && old.Finished {
			m.removeTask(old.ID)
		}
	}
	rt.running = true
	m.leaseFS(src.FS)
	m.leaseFS(dst.FS)

	opts := mirror.Options{
		UseRsync:  pair.UseRsync,
		StateFile: filepath.Join(mirror.StateDir(), pair.ID+".json"),
	}
	t := m.startTask(TaskMirror, 0, func(prog *fileops.Progress) *fileops.Result {
		return mirror.Run(src, dst, opts, prog)
	})
	t.Label = m.mirrorLabel(pair)
	t.notifyErrorsOnly = true
	t.onFinish = func(t *Task) {
		rt.running = false
		rt.lastRun = time.Now()
		rt.lastErr = ""
		if t.ErrorCount > 0 && t.LastError != nil {
			rt.lastErr = t.LastError.Error()
		}
		m.releaseFS(src.FS)
		m.releaseFS(dst.FS)
	}
	rt.taskID, rt.hasTask = t.ID, true
	if foreground {
		m.dialog = Dialog{Kind: DialogProgress, Title: "Mirror", TaskID: t.ID}
	}
}

func (m *Model) removeTask(id int) {
	for i, t := range m.tasks {
		if t.ID == id {
			m.tasks = append(m.tasks[:i], m.tasks[i+1:]...)
			return
		}
	}
}

// --- open sources shared with running mirrors --------------------------------

// leaseFS/releaseFS count the running mirrors using a source, so that
// switching a pane to another source doesn't close a connection a mirror
// is still using: replaceFS defers the Close to the last releaseFS.
func (m *Model) leaseFS(fs vfs.FileSystem) {
	if fs.Kind() == vfs.KindLocal {
		return
	}
	if m.fsLeases == nil {
		m.fsLeases = map[vfs.FileSystem]int{}
	}
	m.fsLeases[fs]++
}

func (m *Model) releaseFS(fs vfs.FileSystem) {
	if fs.Kind() == vfs.KindLocal {
		return
	}
	m.fsLeases[fs]--
	if m.fsLeases[fs] > 0 {
		return
	}
	delete(m.fsLeases, fs)
	if m.fsCloseLater[fs] {
		delete(m.fsCloseLater, fs)
		fs.Close()
	}
}

// closeFSWhenUnused closes fs now, or once the last mirror using it ends.
func (m *Model) closeFSWhenUnused(fs vfs.FileSystem) {
	if m.fsLeases[fs] > 0 {
		if m.fsCloseLater == nil {
			m.fsCloseLater = map[vfs.FileSystem]bool{}
		}
		m.fsCloseLater[fs] = true
		return
	}
	fs.Close()
}

// --- creating a mirror (Ctrl+Alt+M) ------------------------------------------

// doMirrorPaste turns the clipboard into mirror pairs targeting the active
// pane's folder, after confirmation; with an empty clipboard it opens the
// list of saved mirrors instead.
func (m *Model) doMirrorPaste() {
	cb, err := m.effectiveClipboard()
	if err != nil {
		m.setError("Can't mirror: %v", err)
		return
	}
	if cb.Empty() {
		m.openMirrorList()
		return
	}
	p := m.activePane()
	if p.Mode != PaneNormal {
		return
	}
	var pending []config.MirrorPair
	var lines []string
	for _, name := range cb.Names {
		srcPath := cb.FS.Join(cb.Dir, name)
		dstPath := p.FS.Join(p.Path, name)
		src, dst := endpointFor(cb.FS, srcPath), endpointFor(p.FS, dstPath)
		if src.Source == dst.Source && (pathWithin(src.Path, dst.Path) || pathWithin(dst.Path, src.Path)) {
			m.setError("Can't mirror %s into itself: pick a destination outside it", name)
			return
		}
		for _, existing := range m.cfg.MirrorPairs {
			if existing.Dst.Source == dst.Source && existing.Dst.Path == dst.Path {
				m.setError("%s is already the destination of a mirror", dstPath)
				return
			}
		}
		pending = append(pending, config.MirrorPair{ID: newMirrorID(), Src: src, Dst: dst})
		line := srcPath + "\n  → " + dstPath
		if _, err := p.FS.Stat(dstPath); err == nil {
			line += "  (exists: will be overwritten)"
		}
		lines = append(lines, line)
	}

	d := Dialog{
		Kind: DialogMirrorConfirm, Title: "Automatic mirror",
		Message:       strings.Join(lines, "\n"),
		MirrorPending: pending,
	}
	if mirror.CanUseRsync(cb.FS, p.FS) {
		d.Items = []string{"rsync — delta transfer (only changed blocks)", "Generic — copy changed files whole"}
	}
	m.dialog = d
}

// confirmMirror saves the pending pairs and starts their first sync.
func (m *Model) confirmMirror() {
	d := m.dialog
	m.dialog = Dialog{}
	useRsync := len(d.Items) > 0 && d.ItemIdx == 0
	for i, pair := range d.MirrorPending {
		pair.UseRsync = useRsync
		m.cfg.MirrorPairs = append(m.cfg.MirrorPairs, pair)
		src, okS := m.resolveEndpoint(pair.Src)
		dst, okD := m.resolveEndpoint(pair.Dst)
		if okS && okD {
			m.mirrorState(pair.ID).available = true
			m.startMirror(pair, src, dst, i == 0)
		}
	}
	if err := m.cfg.Save(); err != nil {
		m.setError("Could not save the mirror: %v", err)
		return
	}
	if m.dialog.Kind == DialogNone {
		m.setStatus("%d mirror(s) saved", len(d.MirrorPending))
	}
}

func newMirrorID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// pathWithin reports whether path is dir or inside it ("/"-separated,
// which every backend uses; "" is a mount root).
func pathWithin(path, dir string) bool {
	path, dir = strings.TrimSuffix(path, "/"), strings.TrimSuffix(dir, "/")
	return dir == "" || path == dir || strings.HasPrefix(path, dir+"/")
}

// --- list of saved mirrors ----------------------------------------------------

func (m *Model) openMirrorList() {
	idx := 0
	if m.dialog.Kind == DialogMirrorList {
		idx = m.dialog.ItemIdx
	}
	m.dialog = Dialog{Kind: DialogMirrorList, Title: "Automatic mirrors", Items: m.mirrorListItems()}
	if idx < len(m.dialog.Items) {
		m.dialog.ItemIdx = idx
	}
}

func (m *Model) mirrorListItems() []string {
	items := make([]string, len(m.cfg.MirrorPairs))
	for i, pair := range m.cfg.MirrorPairs {
		rt := m.mirrorState(pair.ID)
		status := "waiting for sources"
		switch {
		case pair.Paused:
			status = "paused"
		case rt.running:
			status = "syncing"
		case rt.lastErr != "":
			status = "error: " + rt.lastErr
		case !rt.lastRun.IsZero():
			status = "synced " + rt.lastRun.Format("15:04")
		}
		backend := "generic"
		if pair.UseRsync {
			backend = "rsync"
		}
		items[i] = fmt.Sprintf("[%s, %s] %s", backend, status, m.mirrorLabel(pair))
	}
	return items
}

// updateMirrorListKey handles the mirror list's own keys: Enter syncs the
// selected pair now, p pauses/resumes it, x deletes it (after asking).
func (m *Model) updateMirrorListKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	d := &m.dialog
	if d.ItemIdx < 0 || d.ItemIdx >= len(m.cfg.MirrorPairs) {
		return nil, false
	}
	pair := &m.cfg.MirrorPairs[d.ItemIdx]
	switch msg.String() {
	case "enter":
		src, okS := m.resolveEndpoint(pair.Src)
		dst, okD := m.resolveEndpoint(pair.Dst)
		switch {
		case m.mirrorState(pair.ID).running:
			m.setStatus("Already syncing")
		case !okS || !okD:
			m.setError("Not available now: mount or open both sources first")
		default:
			m.startMirror(*pair, src, dst, true)
		}
		return nil, true
	case "p":
		pair.Paused = !pair.Paused
		m.cfg.Save()
		m.openMirrorList()
		return nil, true
	case "x", "delete":
		m.dialog = Dialog{
			Kind: DialogMirrorConfirmDelete, Title: "Delete mirror",
			Message:      fmt.Sprintf("Stop mirroring %s?\nNo files are deleted.", m.mirrorLabel(*pair)),
			MirrorPairID: pair.ID,
		}
		return nil, true
	}
	return nil, false
}

// deleteMirror removes a saved pair and its state file (the mirrored
// files themselves stay where they are).
func (m *Model) deleteMirror(id string) {
	pairs := m.cfg.MirrorPairs[:0]
	for _, p := range m.cfg.MirrorPairs {
		if p.ID != id {
			pairs = append(pairs, p)
		}
	}
	m.cfg.MirrorPairs = pairs
	if err := m.cfg.Save(); err != nil {
		m.setError("Could not save the configuration: %v", err)
	}
	os.Remove(filepath.Join(mirror.StateDir(), id+".json"))
	if rt := m.mirrors[id]; rt == nil || !rt.running {
		delete(m.mirrors, id)
	}
	m.openMirrorList()
}
