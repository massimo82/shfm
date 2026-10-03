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

//go:build cloud

package cloud

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"

	"shfm/internal/vfs"
)

func init() { Available = true }

// The OAuth clients built into shfm, one per service: empty unless set at
// build time, e.g.
//
//	go build -tags cloud -ldflags "-X shfm/internal/cloud.dropboxAppKey=..." .
//
// Without one, the user registers their own and enters it when adding an
// account (see README, "Cloud storage").
var (
	googleClientID     string
	googleClientSecret string
	dropboxAppKey      string
	oneDriveClientID   string
)

// provider is a supported service: how to authorize an account and talk
// to its API.
type provider struct {
	info ProviderInfo

	endpoint   func() oauth2.Endpoint // a func, so that tests can redirect it
	scopes     []string
	authParams []oauth2.AuthCodeOption

	// The loopback redirect: its host name as registered with the
	// service, and its port (0: any free one).
	redirectHost string
	redirectPort int
	// noRedirectOK: without a redirect, the service shows the code for the
	// user to paste (Dropbox), so a busy port isn't fatal.
	noRedirectOK bool

	newBackend    func(client *http.Client, ts oauth2.TokenSource) backend
	caps          caps
	defaultSecret func() string
}

var providers = map[string]*provider{}

// providerOrder is the order the UI offers the services in.
var providerOrder = []string{GoogleDrive, Dropbox, OneDrive}

func register(p *provider) { providers[p.info.ID] = p }

func (p *provider) oauthConfig(clientID, clientSecret string) *oauth2.Config {
	return &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, Endpoint: p.endpoint(), Scopes: p.scopes}
}

// open returns acc's backend, authorized with tok (refreshed, and saved,
// as needed).
func (p *provider) open(acc Account, cfg *oauth2.Config, tok *oauth2.Token) (backend, caps, error) {
	ts := newSavingTokenSource(cfg, acc.ID, tok)
	return p.newBackend(authClient(ts), ts), p.caps, nil
}

// Providers lists the supported services, in the order the UI offers
// them.
func Providers() []ProviderInfo {
	out := make([]ProviderInfo, len(providerOrder))
	for i, id := range providerOrder {
		out[i] = providers[id].info
	}
	return out
}

// Provider returns the service with the given ID.
func Provider(id string) (ProviderInfo, bool) {
	p, ok := providers[id]
	if !ok {
		return ProviderInfo{}, false
	}
	return p.info, true
}

// Dial opens acc as a source, checking that its authorization still
// works. It makes a request to the service, so it blocks: never call it
// from the UI's event loop. A revoked or expired authorization is
// reported as ErrAuthorization.
func Dial(acc Account) (vfs.FileSystem, error) {
	p, ok := providers[acc.Provider]
	if !ok {
		return nil, fmt.Errorf("unknown cloud service %q", acc.Provider)
	}
	clientID, secret := acc.ClientID, acc.ClientSecret
	if clientID == "" {
		clientID = p.info.DefaultClientID
		if p.defaultSecret != nil {
			secret = p.defaultSecret()
		}
	}
	if clientID == "" {
		return nil, fmt.Errorf("%w: no client ID", ErrAuthorization)
	}
	tok, err := loadToken(acc.ID)
	if err != nil {
		return nil, err
	}
	b, c, err := p.open(acc, p.oauthConfig(clientID, secret), tok)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := b.account(ctx); err != nil {
		return nil, err
	}
	return newFS(acc, b, c), nil
}
