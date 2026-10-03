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
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"shfm/internal/applog"
	"shfm/internal/cloud"
	"shfm/internal/config"
	"shfm/internal/opener"
	"shfm/internal/secret"
	"shfm/internal/vfs"
)

// Cloud storage sources (Google Drive, Dropbox, Microsoft OneDrive — see
// internal/cloud), kept apart from the remote (SMB/NFS/SFTP) ones: saved
// in the configuration's own list (config.CloudSource), listed in the
// source picker's own "Cloud" section, and added through an OAuth
// authorization in the browser instead of a connection form. Without the
// cloud module (built without the "cloud" tag) none of this shows.
//
// Adding an account: a form asks for the OAuth client (the one built into
// shfm, or one the user registered), then shfm opens the service's
// authorization page in the browser — or shows its address, to open on
// another machine and paste back where the browser ended up — and, once
// authorized, opens the account in the active pane and saves it.

// cloudIcon is the source picker's icon for a service.
func cloudIcon(provider string) string {
	switch provider {
	case cloud.GoogleDrive:
		return iconSourceGDrive
	case cloud.Dropbox:
		return iconSourceDropbox
	default:
		return iconSourceOneDrive
	}
}

// cloudMenuEntries are the source picker's "Cloud" section: the saved
// accounts, then one "new account" action per service.
func (m *Model) cloudMenuEntries() []sourceMenuEntry {
	if !cloud.Available {
		return nil
	}
	var entries []sourceMenuEntry
	for _, c := range m.cfg.CloudSources {
		p, ok := cloud.Provider(c.Provider)
		if !ok {
			continue
		}
		label := fmt.Sprintf("%s %s  %s", cloudIcon(c.Provider), p.Name, c.User)
		if c.Name != "" && c.Name != c.User {
			label += "  [" + c.Name + "]"
		}
		entries = append(entries, sourceMenuEntry{label: label, kind: "cloud", cloud: c, provider: p})
	}
	for _, p := range cloud.Providers() {
		entries = append(entries, sourceMenuEntry{
			label: iconSourceAdd + " New " + p.Name + " account…", kind: "new-cloud", provider: p,
		})
	}
	return entries
}

// cloudAccount is a saved account as internal/cloud knows it.
func cloudAccount(c config.CloudSource) (cloud.Account, error) {
	sec, err := c.DecryptedClientSecret()
	if err != nil {
		return cloud.Account{}, fmt.Errorf("could not decrypt the saved client secret: %w", err)
	}
	return cloud.Account{Provider: c.Provider, ID: c.Account, User: c.User, ClientID: c.ClientID, ClientSecret: sec}, nil
}

// cloudSourceDialer returns a saved account's label (as its source names
// itself) and a function opening it; dial is nil without the cloud module.
func cloudSourceDialer(c config.CloudSource) (string, func() (vfs.FileSystem, error)) {
	label := cloud.Label(c.Provider, c.User)
	if !cloud.Available {
		return label, nil
	}
	return label, func() (vfs.FileSystem, error) {
		acc, err := cloudAccount(c)
		if err != nil {
			return nil, err
		}
		return cloud.Dial(acc)
	}
}

// connectSavedCloud opens a saved account in the active pane. If its
// authorization is no longer valid (revoked, or expired), the account form
// opens instead, to authorize it again.
func (m *Model) connectSavedCloud(c config.CloudSource) {
	acc, err := cloudAccount(c)
	if err != nil {
		m.dialog = Dialog{}
		m.setError("%s: %v", c.Name, err)
		return
	}
	label := cloud.Label(c.Provider, c.User)
	authFailed := false // written by dial, read by onError once dial has returned
	m.startConnect(m.active, label, func() (vfs.FileSystem, error) {
		fs, err := cloud.Dial(acc)
		authFailed = errors.Is(err, cloud.ErrAuthorization)
		return fs, err
	}, nil, func(errText string) {
		if p, ok := cloud.Provider(c.Provider); ok && authFailed {
			m.openCloudAccountForm(p, c, "The authorization of "+label+" is no longer valid: authorize it again")
			return
		}
		m.setError("Connecting to %s failed: %s", label, errText)
	})
}

// openCloudAccountForm asks for the OAuth client to authorize an account
// of provider with — again for src, when it's a saved account (src.Account
// set), pre-filled with its client.
func (m *Model) openCloudAccountForm(p cloud.ProviderInfo, src config.CloudSource, errText string) {
	sec, _ := src.DecryptedClientSecret()
	m.dialog = newCloudAccountDialog(p, src, src.ClientID, sec, errText)
}

func newCloudAccountDialog(p cloud.ProviderInfo, src config.CloudSource, clientID, clientSecret, errText string) Dialog {
	inputs := make([]textinput.Model, 2)
	for i := range inputs {
		ti := textinput.New()
		ti.SetWidth(36)
		ti.CharLimit = 512
		inputs[i] = ti
	}
	inputs[0].Placeholder = "client ID"
	if p.DefaultClientID != "" {
		inputs[0].Placeholder = "empty: shfm's own"
	}
	inputs[1].Placeholder = "client secret"
	if !p.NeedsSecret {
		inputs[1].Placeholder = "optional"
	}
	inputs[1].EchoMode = textinput.EchoPassword
	inputs[1].EchoCharacter = '•'
	inputs[0].SetValue(clientID)
	inputs[1].SetValue(clientSecret)
	inputs[0].Focus()
	title := "Add a " + p.Name + " account"
	if src.Account != "" {
		title = "Authorize " + cloud.Label(src.Provider, src.User) + " again"
	}
	return Dialog{
		Kind: DialogConnectCloud, Title: title, Inputs: inputs,
		CloudProvider: p, CloudSource: src,
		Message: errText, IsError: errText != "",
	}
}

func (m *Model) renderCloudAccountForm(b *strings.Builder) string {
	d := m.dialog
	p := d.CloudProvider
	for i, label := range []string{"Client ID", "Client secret"} {
		marker := "  "
		if i == d.FocusIdx {
			marker = "▸ "
		}
		b.WriteString(fmt.Sprintf("%s%-14s %s\n", marker, label+":", d.Inputs[i].View()))
	}
	b.WriteString("\n")
	if p.DefaultClientID != "" {
		b.WriteString(styleDim.Render("Leave the client ID empty to use the one built into shfm, or enter\nan OAuth client of your own.") + "\n")
	} else {
		b.WriteString(styleDim.Render("Register an OAuth client of your own with "+p.Name+" (see README,\n\"Cloud storage\") and enter it here.") + "\n")
	}
	b.WriteString(styleDim.Render("Redirect URI to register: "+p.RedirectURI) + "\n")
	if p.NeedsSecret {
		b.WriteString(styleDim.Render(p.Name+" requires the client secret too.") + "\n")
	}
	b.WriteString(styleDim.Render("(the client secret is saved encrypted)") + "\n")
	if d.IsError && d.Message != "" {
		b.WriteString("\n" + styleErr.Render(d.Message) + "\n")
	}
	b.WriteString("\n" + styleDim.Render("Tab or click field · Enter authorize in the browser · Esc cancel"))
	return dialogBox(72).Render(b.String())
}

// startCloudAuthorization starts authorizing the account the form is for:
// it opens the service's page in the browser (when there is a graphical
// session) and shows the authorization dialog, while the authorization
// completes in the background — through the browser's redirect to shfm,
// or what the user pastes.
func (m *Model) startCloudAuthorization() {
	d := m.dialog
	p, src := d.CloudProvider, d.CloudSource
	clientID := strings.TrimSpace(d.Inputs[0].Value())
	clientSecret := strings.TrimSpace(d.Inputs[1].Value())
	auth, err := cloud.StartAuthorization(p.ID, clientID, clientSecret, src.Account)
	if err != nil {
		m.dialog.IsError, m.dialog.Message = true, err.Error()
		return
	}

	id := m.nextConnectID
	m.nextConnectID++
	ch := m.connectCh
	paneIdx := m.active
	go func() {
		// An authorization left pending (the browser closed, the dialog
		// sent to the background) doesn't keep its local server forever.
		ctx, cancel := context.WithTimeout(context.Background(), cloudAuthTimeout)
		defer cancel()
		acc, err := auth.Wait(ctx)
		var fs vfs.FileSystem
		if err == nil {
			fs, err = cloud.Dial(acc)
		}
		cancelled := errors.Is(err, context.Canceled)
		if errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("no answer from the browser in time: start again")
		}
		ch <- connectResultMsg{
			requestID: id, paneIndex: paneIdx, label: cloud.Label(p.ID, acc.User), fs: fs, err: err,
			onSuccess: func() { m.saveCloudSource(acc, src) },
			onError: func(errText string) {
				if cancelled {
					m.setStatus("Authorization cancelled")
					return
				}
				m.dialog = newCloudAccountDialog(p, src, clientID, clientSecret, "The authorization failed: "+errText)
			},
		}
	}()

	opened := graphicalSession() && openInBrowser(auth.URL())
	in := textinput.New()
	in.Placeholder = "address or code"
	in.SetWidth(60)
	in.CharLimit = 4096
	in.Focus()
	m.dialog = Dialog{
		Kind: DialogCloudAuth, Title: "Authorize shfm on " + p.Name,
		Inputs: []textinput.Model{in}, CloudProvider: p, CloudSource: src, CloudAuth: auth,
		ConnectRequestID: id, CloudBrowser: opened,
	}
}

// cloudAuthTimeout is how long an authorization may wait for the user.
const cloudAuthTimeout = 30 * time.Minute

// openInBrowser opens url with the desktop's web browser, reporting
// whether one could be launched.
func openInBrowser(url string) bool {
	app, ok := opener.DefaultApp("x-scheme-handler/https")
	if !ok {
		return false
	}
	if err := opener.Launch(app, url); err != nil {
		applog.Warn("could not open the browser", "app", app.Name, "error", err)
		return false
	}
	return true
}

func (m *Model) renderCloudAuth(b *strings.Builder) string {
	d := m.dialog
	w := min(max(m.width-8, 64), 110)
	if d.CloudBrowser {
		b.WriteString("The authorization page is open in your browser: allow shfm access\nthere. Or open this address yourself:\n\n")
	} else {
		b.WriteString("Open this address in a browser and allow shfm access:\n\n")
	}
	// A hyperlink too, so that terminals supporting them (OSC 8) open it
	// on a click, wrapped over several lines as it is.
	b.WriteString(styleAccent.Hyperlink(d.CloudAuth.URL()).Render(d.CloudAuth.URL()) + "\n\n")
	b.WriteString(styleDim.Render("Waiting for the browser to come back to shfm. If it runs on another\n"+
		"machine, paste here the address it ends up on (or the code it shows):") + "\n")
	b.WriteString(d.Inputs[0].View() + "\n")
	if d.Message != "" {
		b.WriteString("\n" + styleErr.Render(d.Message) + "\n")
	}
	b.WriteString("\n" + styleDim.Render("Enter submit what you pasted · Esc cancel"))
	return dialogBox(w).Render(b.String())
}

// updateCloudAuthKey handles Esc in the authorization dialog: it abandons
// the authorization (the background wait then ends).
func (m *Model) updateCloudAuthKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if msg.String() != "esc" {
		return nil, false
	}
	if m.dialog.CloudAuth != nil {
		m.dialog.CloudAuth.Cancel()
	}
	m.dialog = Dialog{}
	return nil, true
}

// submitCloudAuth hands what the user pasted to the authorization.
func (m *Model) submitCloudAuth() {
	d := m.dialog
	pasted := strings.TrimSpace(d.Inputs[0].Value())
	if pasted == "" {
		m.dialog.Message = "Paste the address the browser ended up on, or the code it shows"
		return
	}
	d.CloudAuth.Submit(pasted)
	m.dialog = Dialog{
		Kind: DialogConnecting, Title: "Connecting",
		Message: "Completing the authorization on " + d.CloudProvider.Name + "…", ConnectRequestID: d.ConnectRequestID,
	}
}

// saveCloudSource saves a newly authorized account, or updates the saved
// one it authorized again (keeping the name the user gave it).
func (m *Model) saveCloudSource(acc cloud.Account, prev config.CloudSource) {
	src := config.CloudSource{Name: acc.User, Provider: acc.Provider, User: acc.User, Account: acc.ID, ClientID: acc.ClientID}
	if acc.ClientSecret != "" {
		enc, err := secret.Encrypt(acc.ClientSecret)
		if err != nil {
			m.setError("Could not encrypt the client secret: %v", err)
			return
		}
		src.EncryptedClientSecret = enc
	}
	for i, c := range m.cfg.CloudSources {
		if c.Account == acc.ID || (c.Provider == acc.Provider && c.User == acc.User) {
			if c.Name != "" && c.Name != c.User {
				src.Name = c.Name
			}
			m.cfg.CloudSources[i] = src
			m.cfg.Save()
			return
		}
	}
	if prev.Name != "" && prev.Name != prev.User {
		src.Name = prev.Name
	}
	m.cfg.CloudSources = append(m.cfg.CloudSources, src)
	m.cfg.Save()
}

// cloudRemovalMessage is the confirmation asked before removing a saved
// account.
func (m *Model) cloudRemovalMessage(c config.CloudSource) string {
	return fmt.Sprintf("Remove the account %s?\n\nshfm deletes its authorization token and forgets it. "+
		"The authorization itself can be revoked in the account's security settings on the service's "+
		"website.", cloud.Label(c.Provider, c.User))
}

// removeCloudSource deletes a saved account and its token.
func (m *Model) removeCloudSource(c config.CloudSource) {
	for i, existing := range m.cfg.CloudSources {
		if existing.Account == c.Account {
			m.cfg.CloudSources = append(m.cfg.CloudSources[:i], m.cfg.CloudSources[i+1:]...)
			if err := m.cfg.Save(); err != nil {
				m.setError("Could not save the configuration: %v", err)
				return
			}
			if err := cloud.Forget(c.Account); err != nil {
				m.setError("Removed %s, but its token couldn't be deleted: %v", c.Name, err)
				return
			}
			m.setStatus("Removed %s", cloud.Label(c.Provider, c.User))
			return
		}
	}
}
