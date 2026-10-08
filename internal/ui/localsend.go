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
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"shfm/internal/applog"
	"shfm/internal/config"
	"shfm/internal/fileops"
	"shfm/internal/localsend"
	"shfm/internal/notify"
	"shfm/internal/secret"
	"shfm/internal/vfs"
)

// LocalSend (internal/localsend, built with the "localsend" tag): the
// devices dialog sends the selection, or a text, to a device found on
// the network, and turns receiving on and off. What other devices send
// is asked in a dialog shown over whatever is on screen, like the
// polkit agent's (see auth.go), as is the PIN a device asks for.

// The messages the Service's goroutines send through Model.lsCh.
type (
	lsChangedMsg   struct{}
	lsIncomingMsg  struct{ req *localsend.Request }
	lsWithdrawnMsg struct{ req *localsend.Request }
	lsPINAskMsg    struct {
		dev   localsend.Device
		wrong bool
		reply chan lsPINReply
	}
)

type lsPINReply struct {
	pin string
	ok  bool
}

// lsOverlay is a LocalSend dialog shown over the others: an incoming
// request, a message, or a PIN to give.
type lsOverlay struct {
	dlg   Dialog
	prev  Dialog
	req   *localsend.Request // DialogLocalSendIncoming, DialogLocalSendMessage
	reply chan lsPINReply    // DialogLocalSendPIN
}

// lsMaxListed is how many files of an incoming request are listed.
const lsMaxListed = 6

func isLocalSendDialog(k DialogKind) bool {
	switch k {
	case DialogLocalSend, DialogLocalSendText, DialogLocalSendSetPIN,
		DialogLocalSendIncoming, DialogLocalSendMessage, DialogLocalSendPIN:
		return true
	}
	return false
}

// lsSettings are the LocalSend settings from the configuration.
func (m *Model) lsSettings() localsend.Settings {
	pin, err := secret.Decrypt(m.cfg.LocalSendPIN)
	if err != nil {
		applog.Warn("localsend: PIN unreadable", "error", err)
	}
	return localsend.Settings{Alias: m.cfg.LocalSendAlias, Port: m.cfg.LocalSendPort, Receive: m.cfg.LocalSendReceive, PIN: pin}
}

// startLocalSend starts the Service, if it isn't running.
func (m *Model) startLocalSend() error {
	if m.ls != nil {
		return nil
	}
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	ch := m.lsCh
	ev := localsend.Events{
		Changed: func() {
			select {
			case ch <- lsChangedMsg{}:
			default: // one pending is enough
			}
		},
		Incoming:  func(r *localsend.Request) { go func() { ch <- lsIncomingMsg{req: r} }() },
		Withdrawn: func(r *localsend.Request) { go func() { ch <- lsWithdrawnMsg{req: r} }() },
	}
	svc, err := localsend.Start(filepath.Join(dir, "localsend"), m.lsSettings(), ev)
	if err != nil {
		return err
	}
	m.ls = svc
	return nil
}

// StopLocalSend stops LocalSend, once shfm is over.
func (m *Model) StopLocalSend() {
	if m.ls != nil {
		m.ls.Close()
		m.ls = nil
	}
}

func (m *Model) waitForLocalSendMsg() tea.Cmd {
	ch := m.lsCh
	return func() tea.Msg { return <-ch }
}

// lsPINAsker asks the PIN a device wants, from a task's goroutine.
func (m *Model) lsPINAsker() localsend.PINAsker {
	ch := m.lsCh
	return func(dev localsend.Device, wrong bool) (string, bool) {
		reply := make(chan lsPINReply, 1)
		ch <- lsPINAskMsg{dev: dev, wrong: wrong, reply: reply}
		r := <-reply
		return r.pin, r.ok
	}
}

// openLocalSend opens the devices dialog, to send the active pane's
// selection (or the entry under the cursor).
func (m *Model) openLocalSend() {
	if !localsend.Available {
		m.setError("%v", localsend.ErrUnavailable)
		return
	}
	if err := m.startLocalSend(); err != nil {
		m.setError("LocalSend: %v", err)
		return
	}
	p := m.activePane()
	d := Dialog{Kind: DialogLocalSend, Title: "LocalSend"}
	if p.Mode == PaneNormal {
		d.LSFS, d.LSDir, d.LSNames = p.FS, p.Path, p.SelectedNames()
	}
	m.dialog = d
	m.refreshLocalSendDevices()
}

// refreshLocalSendDevices updates the devices dialog's list, keeping the
// device selected.
func (m *Model) refreshLocalSendDevices() {
	d := &m.dialog
	if d.Kind != DialogLocalSend || m.ls == nil {
		return
	}
	selected := ""
	if d.ItemIdx >= 0 && d.ItemIdx < len(d.LSDevices) {
		selected = d.LSDevices[d.ItemIdx].Fingerprint
	}
	d.LSDevices = m.ls.Devices()
	d.Items = make([]string, len(d.LSDevices))
	d.ItemIdx = 0
	for i, dev := range d.LSDevices {
		d.Items[i] = dev.Label() + "  " + dev.IP
		if dev.Fingerprint == selected {
			d.ItemIdx = i
		}
	}
}

func (m *Model) selectedLocalSendDevice() (localsend.Device, bool) {
	d := m.dialog
	if d.ItemIdx < 0 || d.ItemIdx >= len(d.LSDevices) {
		return localsend.Device{}, false
	}
	return d.LSDevices[d.ItemIdx], true
}

func (m *Model) updateLocalSendKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	d := &m.dialog
	k := msg.String()
	switch d.Kind {
	case DialogLocalSend:
		switch k {
		case "enter":
			m.sendToLocalSendDevice()
		case "t":
			if dev, ok := m.selectedLocalSendDevice(); ok {
				in := newSingleInputDialog(DialogLocalSendText, "Message to "+dev.Alias, "text", "")
				in.Inputs[0].CharLimit = 0
				in.Inputs[0].SetWidth(56)
				in.LSDevices, in.ItemIdx = []localsend.Device{dev}, 0
				in.LSFS, in.LSDir, in.LSNames = d.LSFS, d.LSDir, d.LSNames
				m.dialog = in
			}
		case "r":
			m.ls.Rescan()
		case "ctrl+r":
			m.cfg.LocalSendReceive = !m.cfg.LocalSendReceive
			m.saveLocalSendConfig()
		case "ctrl+p":
			in := newSingleInputDialog(DialogLocalSendSetPIN, "PIN to receive", "no PIN", "")
			in.Inputs[0].CharLimit = 32
			if pin, err := secret.Decrypt(m.cfg.LocalSendPIN); err == nil {
				in.Inputs[0].SetValue(pin)
				in.Inputs[0].CursorEnd()
			}
			in.LSFS, in.LSDir, in.LSNames = d.LSFS, d.LSDir, d.LSNames
			m.dialog = in
		default:
			return nil, false
		}
		return nil, true

	case DialogLocalSendText, DialogLocalSendSetPIN:
		switch k {
		case "enter":
			value := d.Inputs[0].Value()
			back := Dialog{Kind: DialogLocalSend, Title: "LocalSend", LSFS: d.LSFS, LSDir: d.LSDir, LSNames: d.LSNames}
			if d.Kind == DialogLocalSendText {
				if strings.TrimSpace(value) == "" {
					return nil, true
				}
				m.sendLocalSendText(d.LSDevices[0], value)
				return nil, true
			}
			enc, err := secret.Encrypt(strings.TrimSpace(value))
			if err != nil {
				d.Message = "Could not save the PIN: " + err.Error()
				return nil, true
			}
			m.cfg.LocalSendPIN = enc
			m.saveLocalSendConfig()
			m.dialog = back
			m.refreshLocalSendDevices()
			return nil, true
		case "esc":
			m.dialog = Dialog{Kind: DialogLocalSend, Title: "LocalSend", LSFS: d.LSFS, LSDir: d.LSDir, LSNames: d.LSNames}
			m.refreshLocalSendDevices()
			return nil, true
		}
		return nil, false

	case DialogLocalSendIncoming:
		switch k {
		case "enter":
			m.answerIncoming(d.ItemIdx)
			return nil, true
		case "esc":
			m.answerIncoming(-1)
			return nil, true
		}
		return nil, false

	case DialogLocalSendMessage:
		switch k {
		case "enter":
			if d.ItemIdx == 0 {
				m.copyText(m.lsOverlay.req.Message)
			}
			m.closeLocalSendOverlay()
			return nil, true
		case "esc":
			m.closeLocalSendOverlay()
			return nil, true
		}
		return nil, false

	case DialogLocalSendPIN:
		switch k {
		case "enter":
			m.answerLocalSendPIN(lsPINReply{pin: strings.TrimSpace(d.Inputs[0].Value()), ok: true})
			return nil, true
		case "esc":
			m.answerLocalSendPIN(lsPINReply{})
			return nil, true
		}
		var cmd tea.Cmd
		d.Inputs[0], cmd = d.Inputs[0].Update(msg)
		return cmd, true
	}
	return nil, false
}

// saveLocalSendConfig saves the LocalSend settings and applies them.
func (m *Model) saveLocalSendConfig() {
	if err := m.cfg.Save(); err != nil {
		m.setError("Could not save the configuration: %v", err)
	}
	if m.ls != nil {
		m.ls.Update(m.lsSettings())
	}
}

// sendToLocalSendDevice sends the dialog's entries to its selected device.
func (m *Model) sendToLocalSendDevice() {
	d := m.dialog
	dev, ok := m.selectedLocalSendDevice()
	if !ok {
		return
	}
	if len(d.LSNames) == 0 {
		m.setStatus("Nothing to send: select files first (or press t for a message)")
		return
	}
	items := make([]fileops.Item, len(d.LSNames))
	for i, n := range d.LSNames {
		items[i] = fileops.Item{FS: d.LSFS, Path: d.LSFS.Join(d.LSDir, n)}
	}
	svc, ask := m.ls, m.lsPINAsker()
	t := m.startTask(TaskSend, len(items), func(prog *fileops.Progress) *fileops.Result {
		return svc.Send(dev, items, ask, prog)
	})
	t.Label = "to " + dev.Alias
	m.activePane().DeselectAll()
	m.dialog = Dialog{Kind: DialogProgress, Title: "Send to " + dev.Alias, TaskID: t.ID}
}

func (m *Model) sendLocalSendText(dev localsend.Device, text string) {
	svc, ask := m.ls, m.lsPINAsker()
	t := m.startTask(TaskSend, 1, func(prog *fileops.Progress) *fileops.Result {
		return svc.SendText(dev, text, ask, prog)
	})
	t.Label = "message to " + dev.Alias
	m.dialog = Dialog{}
	m.setStatus("Sending the message to %s… (Ctrl+B: tasks)", dev.Alias)
}

// handleLocalSendMsg handles a message from the Service.
func (m *Model) handleLocalSendMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case lsChangedMsg:
		m.refreshLocalSendDevices()
	case lsIncomingMsg:
		m.lsQueue = append(m.lsQueue, msg.req)
		m.notifyIncoming(msg.req)
		m.showNextIncoming()
	case lsWithdrawnMsg:
		for i, r := range m.lsQueue {
			if r == msg.req {
				m.lsQueue = append(m.lsQueue[:i], m.lsQueue[i+1:]...)
				break
			}
		}
		if o := m.lsOverlay; o != nil && o.req == msg.req {
			m.closeLocalSendOverlay()
			m.setStatus("%s withdrew the transfer", msg.req.From.Alias)
		}
	case lsPINAskMsg:
		if m.lsOverlay != nil && m.lsOverlay.reply != nil {
			// One PIN at a time: tasks send one after another.
			m.lsOverlay.reply <- lsPINReply{}
			m.closeLocalSendOverlay()
		}
		ti := textinput.New()
		ti.CharLimit = 32
		ti.SetWidth(20)
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
		ti.Focus()
		d := Dialog{Kind: DialogLocalSendPIN, Title: "PIN for " + msg.dev.Alias, Inputs: []textinput.Model{ti}}
		if msg.wrong {
			d.Message = "Wrong PIN, try again."
		}
		m.openLocalSendOverlay(&lsOverlay{dlg: d, reply: msg.reply})
	}
	return m.waitForLocalSendMsg()
}

// notifyIncoming tells the desktop about a request: shfm may be in a
// terminal out of sight.
func (m *Model) notifyIncoming(r *localsend.Request) {
	if m.cfg != nil && !m.cfg.Notifications {
		return
	}
	body := fmt.Sprintf("%s wants to send %s", r.From.Alias, lsWhat(r))
	if r.IsMessage {
		body = "Message from " + r.From.Alias
	}
	go func() {
		if err := notify.Send("shfm: LocalSend", body, notify.Normal); err != nil {
			applog.Debug("desktop notification failed", "error", err)
		}
	}()
}

func lsWhat(r *localsend.Request) string {
	if len(r.Files) == 1 {
		return fmt.Sprintf("%q (%s)", r.Files[0].Name, humanSize(r.Size))
	}
	return fmt.Sprintf("%d files (%s)", len(r.Files), humanSize(r.Size))
}

// showNextIncoming shows the next request waiting, unless one is shown.
func (m *Model) showNextIncoming() {
	if m.lsOverlay != nil || len(m.lsQueue) == 0 {
		return
	}
	r := m.lsQueue[0]
	m.lsQueue = m.lsQueue[1:]
	if r.IsMessage {
		m.openLocalSendOverlay(&lsOverlay{req: r, dlg: Dialog{
			Kind: DialogLocalSendMessage, Title: "Message from " + r.From.Alias,
			Items: []string{"Copy to clipboard", "Close"},
		}})
		return
	}
	items := []string{"Save in " + displayPath(m.downloadsDir()), "Decline"}
	if p := m.activePane(); p.Mode == PaneNormal {
		items = []string{items[0], "Save here, in " + truncate(p.Path, 40), items[1]}
	}
	m.openLocalSendOverlay(&lsOverlay{req: r, dlg: Dialog{
		Kind: DialogLocalSendIncoming, Title: "LocalSend: " + r.From.Alias, Items: items,
	}})
}

// downloadsDir is the user's Downloads folder.
func (m *Model) downloadsDir() string {
	home := homeOrRoot()
	for path, key := range xdgUserDirs(home) {
		if key == "XDG_DOWNLOAD_DIR" {
			return path
		}
	}
	return filepath.Join(home, "Downloads")
}

func displayPath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}

// answerIncoming answers the request shown with its choice idx: save in
// Downloads, here (when offered), decline (-1, or the last).
func (m *Model) answerIncoming(idx int) {
	o := m.lsOverlay
	if o == nil || o.req == nil {
		return
	}
	r := o.req
	n := len(o.dlg.Items)
	m.closeLocalSendOverlay()
	if idx < 0 || idx >= n-1 {
		r.Decline()
		m.setStatus("Declined what %s sends", r.From.Alias)
		return
	}
	var fs vfs.FileSystem
	var dir string
	if idx == 0 {
		dir = m.downloadsDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			r.Decline()
			m.setError("Could not create %s: %v", dir, err)
			return
		}
		fs = vfs.NewLocalFS("Local", dir)
	} else {
		p := m.activePane()
		fs, dir = p.FS, p.Path
	}
	t := m.startTask(TaskReceive, len(r.Files), func(prog *fileops.Progress) *fileops.Result {
		return r.Accept(fs, dir, prog)
	})
	t.Label = "from " + r.From.Alias
	if m.dialog.Kind == DialogNone {
		m.dialog = Dialog{Kind: DialogProgress, Title: "Receive from " + r.From.Alias, TaskID: t.ID}
	} else {
		m.setStatus("Receiving from %s… (Ctrl+B: tasks)", r.From.Alias)
	}
}

func (m *Model) answerLocalSendPIN(r lsPINReply) {
	o := m.lsOverlay
	if o == nil || o.reply == nil {
		return
	}
	o.reply <- r
	m.closeLocalSendOverlay()
}

// copyText puts text in the desktop's clipboard: Wayland's, or the
// terminal's (OSC 52).
func (m *Model) copyText(text string) {
	if m.sysclip != nil {
		data := []byte(text)
		err := m.sysclip.SetSelection(map[string][]byte{
			"text/plain;charset=utf-8": data, "text/plain": data, "UTF8_STRING": data,
		})
		if err == nil {
			m.setStatus("Message copied")
			return
		}
		applog.Warn("copying to the clipboard failed", "error", err)
	}
	m.queueCmd(tea.SetClipboard(text))
	m.setStatus("Message copied")
}

func (m *Model) openLocalSendOverlay(o *lsOverlay) {
	o.prev = m.dialog
	if isLocalSendOverlay(o.prev.Kind) {
		o.prev = Dialog{}
	}
	m.lsOverlay = o
	m.dialog = o.dlg
}

func isLocalSendOverlay(k DialogKind) bool {
	return k == DialogLocalSendIncoming || k == DialogLocalSendMessage || k == DialogLocalSendPIN
}

// closeLocalSendOverlay closes the overlay, back to the dialog under it,
// and shows the next request waiting.
func (m *Model) closeLocalSendOverlay() {
	if m.lsOverlay == nil {
		return
	}
	m.dialog = m.lsOverlay.prev
	m.lsOverlay = nil
	if m.dialog.Kind == DialogLocalSend {
		m.refreshLocalSendDevices()
	}
	m.showNextIncoming()
}

// keepLocalSendOnTop keeps the overlay shown over any dialog opened
// meanwhile (which it then shows once closed); the polkit prompt goes
// over it.
func (m *Model) keepLocalSendOnTop() {
	o := m.lsOverlay
	if o == nil || m.dialog.Kind == DialogAuth {
		return
	}
	if m.dialog.Kind == o.dlg.Kind {
		o.dlg = m.dialog
		return
	}
	o.prev = m.dialog
	m.dialog = o.dlg
}

// lsIncomingHeader is what the incoming request dialog shows above its
// choices.
func lsIncomingHeader(r *localsend.Request) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s (%s) wants to send %s:\n", r.From.Label(), r.From.IP, lsWhat(r)))
	for i, f := range r.Files {
		if i == lsMaxListed {
			b.WriteString(styleDim.Render(fmt.Sprintf("  …and %d more", len(r.Files)-i)) + "\n")
			break
		}
		b.WriteString("  " + truncate(f.Name, 50) + styleDim.Render("  "+humanSize(f.Size)) + "\n")
	}
	return b.String()
}

// lsListTop is the row of a LocalSend dialog's first choice, under the
// title (for clicks: see handleDialogMouse).
func (m *Model) lsListTop() int {
	switch m.dialog.Kind {
	case DialogLocalSendIncoming:
		if m.lsOverlay != nil && m.lsOverlay.req != nil {
			return strings.Count(lsIncomingHeader(m.lsOverlay.req), "\n") + 1
		}
	case DialogLocalSendMessage:
		if m.lsOverlay != nil && m.lsOverlay.req != nil {
			return strings.Count(lsMessageText(m.lsOverlay.req.Message), "\n") + 2
		}
	}
	return 0
}

// lsMessageText is a message as shown: wrapped, at most 16 lines.
func lsMessageText(text string) string {
	wrapped := lipgloss.NewStyle().Width(60).Render(text)
	lines := strings.Split(wrapped, "\n")
	if len(lines) > 16 {
		lines = append(lines[:15], styleDim.Render("… (copy it to read it all)"))
	}
	return strings.Join(lines, "\n")
}

func renderChoices(b *strings.Builder, items []string, idx int) {
	for i, it := range items {
		prefix, s := "  ", styleFile
		if i == idx {
			prefix, s = "\u25b8 ", styleAccent
		}
		b.WriteString(prefix + s.Render(truncate(it, 62)) + "\n")
	}
}

func (m *Model) renderLocalSendDialog(b *strings.Builder) string {
	d := m.dialog
	switch d.Kind {
	case DialogLocalSend:
		renderChoices(b, d.Items, d.ItemIdx)
		if len(d.Items) == 0 {
			b.WriteString(styleDim.Render("No device found yet: LocalSend must be open on it,\non the same network.") + "\n")
		}
		b.WriteString("\n")
		if m.ls != nil {
			st := m.ls.Status()
			if m.ls.Scanning() {
				b.WriteString(styleDim.Render("Looking for devices…") + "\n")
			}
			recv := "off"
			if st.Receive {
				recv = "on"
				if st.PIN {
					recv += ", with PIN"
				}
			}
			b.WriteString(fmt.Sprintf("This device: %s · port %d · receiving %s\n", st.Alias, st.Port, recv))
			if st.Multicast != nil {
				b.WriteString(styleDim.Render("Multicast unavailable: devices found by scanning only") + "\n")
			}
		}
		what := "nothing selected"
		if n := len(d.LSNames); n == 1 {
			what = fmt.Sprintf("sends %q", truncate(d.LSNames[0], 40))
		} else if n > 1 {
			what = fmt.Sprintf("sends %d items", n)
		}
		b.WriteString(styleDim.Render("Enter: "+what+" · t message · r search again") + "\n")
		b.WriteString(styleDim.Render("Ctrl+R receiving on/off · Ctrl+P PIN · Esc close"))
		return dialogBox(68).Render(b.String())

	case DialogLocalSendText, DialogLocalSendSetPIN:
		b.WriteString(d.Inputs[0].View() + "\n\n")
		if d.Message != "" {
			b.WriteString(styleErr.Render(d.Message) + "\n\n")
		}
		hint := "Enter send · Esc back"
		if d.Kind == DialogLocalSendSetPIN {
			hint = "Senders must give it (empty: no PIN) · Enter save · Esc back"
		}
		b.WriteString(styleDim.Render(hint))
		return dialogBox(64).Render(b.String())

	case DialogLocalSendIncoming:
		if m.lsOverlay != nil && m.lsOverlay.req != nil {
			b.WriteString(lsIncomingHeader(m.lsOverlay.req) + "\n")
		}
		renderChoices(b, d.Items, d.ItemIdx)
		b.WriteString("\n" + styleDim.Render("Enter choose · Esc decline"))
		return dialogBox(68).Render(b.String())

	case DialogLocalSendMessage:
		if m.lsOverlay != nil && m.lsOverlay.req != nil {
			b.WriteString(lsMessageText(m.lsOverlay.req.Message) + "\n\n")
		}
		renderChoices(b, d.Items, d.ItemIdx)
		b.WriteString("\n" + styleDim.Render("Enter choose · Esc close"))
		return dialogBox(64).Render(b.String())

	case DialogLocalSendPIN:
		if d.Message != "" {
			b.WriteString(styleErr.Render(d.Message) + "\n\n")
		}
		b.WriteString(d.Inputs[0].View() + "\n\n")
		b.WriteString(styleDim.Render("Enter send · Esc cancel the transfer"))
		return dialogBox(48).Render(b.String())
	}
	return dialogBox(64).Render(b.String())
}
