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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// Authorization is an OAuth 2.0 authorization in progress (authorization
// code flow with PKCE): the user opens URL in a browser and allows shfm
// access; the service then redirects the browser to a small HTTP server
// shfm runs on a loopback address, handing over the code Wait exchanges
// for the account's token.
//
// When the browser runs on another machine (shfm over SSH), the redirect
// can't reach that server: the user copies the address the browser ended
// up on (or, for Dropbox without a redirect, the code it shows) and pastes
// it, which Submit takes instead.
type Authorization struct {
	p         *provider
	cfg       *oauth2.Config
	clientID  string // as the user gave it: empty for the built-in client
	verifier  string
	state     string
	accountID string // the account authorized again, or "" for a new one
	url       string

	codes chan codeResult // the first one wins
	srv   *http.Server
	ln    []net.Listener

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

type codeResult struct {
	code string
	err  error
}

// StartAuthorization starts authorizing an account of provider with the
// OAuth client clientID (empty: the one built into shfm) and its secret,
// if it has one. accountID is the ID of the account to authorize again,
// "" for a new account.
func StartAuthorization(providerID, clientID, clientSecret, accountID string) (*Authorization, error) {
	p, ok := providers[providerID]
	if !ok {
		return nil, fmt.Errorf("unknown cloud service %q", providerID)
	}
	id := clientID
	if id == "" {
		id = p.info.DefaultClientID
	}
	if id == "" {
		return nil, fmt.Errorf("a client ID is needed: register shfm with %s (see README, \"Cloud storage\")", p.info.Name)
	}
	if p.info.NeedsSecret && clientSecret == "" && clientID != "" {
		return nil, fmt.Errorf("%s's client secret is needed too", p.info.Name)
	}

	a := &Authorization{
		p: p, clientID: clientID, accountID: accountID,
		verifier: oauth2.GenerateVerifier(),
		state:    randomState(),
		codes:    make(chan codeResult, 1),
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())

	redirect, err := a.listen()
	if err != nil {
		if p.noRedirectOK {
			// Dropbox can show the code to copy instead.
			redirect = ""
		} else {
			return nil, fmt.Errorf("could not start the local server receiving the authorization: %w", err)
		}
	}
	a.cfg = p.oauthConfig(id, clientSecret)
	a.cfg.RedirectURL = redirect
	opts := append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(a.verifier)}, p.authParams...)
	a.url = a.cfg.AuthCodeURL(a.state, opts...)
	return a, nil
}

func randomState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// listen starts the loopback server and returns the redirect URI pointing
// to it. It listens on both 127.0.0.1 and ::1, on the same port, since a
// browser may resolve "localhost" to either.
func (a *Authorization) listen() (string, error) {
	port := a.p.redirectPort
	ln4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return "", err
	}
	a.ln = append(a.ln, ln4)
	port = ln4.Addr().(*net.TCPAddr).Port
	if ln6, err := net.Listen("tcp6", net.JoinHostPort("::1", strconv.Itoa(port))); err == nil {
		a.ln = append(a.ln, ln6)
	}
	a.srv = &http.Server{Handler: http.HandlerFunc(a.serveRedirect), ReadHeaderTimeout: 10 * time.Second}
	for _, ln := range a.ln {
		go a.srv.Serve(ln)
	}
	return fmt.Sprintf("http://%s:%d/", a.p.redirectHost, port), nil
}

func (a *Authorization) serveRedirect(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	code, err := a.parseRedirect(r.URL.Query())
	msg := "shfm is now authorized: you can close this page and go back to shfm."
	if err != nil {
		msg = "The authorization failed: " + err.Error()
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>shfm</title></head>"+
		"<body style=\"font-family:sans-serif;margin:3em\"><h1>shfm</h1><p>%s</p></body></html>", html.EscapeString(msg))
	a.deliver(codeResult{code: code, err: err})
}

// parseRedirect takes the code out of the redirect's query.
func (a *Authorization) parseRedirect(q url.Values) (string, error) {
	if e := q.Get("error"); e != "" {
		if d := q.Get("error_description"); d != "" {
			e += ": " + d
		}
		return "", fmt.Errorf("%s refused the authorization (%s)", a.p.info.Name, e)
	}
	if s := q.Get("state"); s != a.state {
		return "", errors.New("the answer doesn't belong to this authorization (state mismatch): start again")
	}
	code := q.Get("code")
	if code == "" {
		return "", errors.New("the answer carries no authorization code")
	}
	return code, nil
}

func (a *Authorization) deliver(c codeResult) {
	select {
	case a.codes <- c:
	default: // a result is already waiting
	}
}

// URL is the page the user authorizes shfm on.
func (a *Authorization) URL() string { return a.url }

// Submit hands over what the user pasted: the address the browser was
// redirected to, or just the code.
func (a *Authorization) Submit(pasted string) {
	pasted = strings.TrimSpace(pasted)
	if strings.Contains(pasted, "code=") || strings.Contains(pasted, "error=") {
		raw := pasted
		if i := strings.Index(raw, "?"); i >= 0 {
			raw = raw[i+1:]
		}
		if i := strings.Index(raw, "#"); i >= 0 {
			raw = raw[:i]
		}
		q, err := url.ParseQuery(raw)
		if err != nil {
			a.deliver(codeResult{err: fmt.Errorf("not an address with an authorization code: %w", err)})
			return
		}
		code, err := a.parseRedirect(q)
		a.deliver(codeResult{code: code, err: err})
		return
	}
	a.deliver(codeResult{code: pasted})
}

// Wait waits for the authorization code, exchanges it for the account's
// token, finds out which account it is and saves it. It blocks: never
// call it from the UI's event loop.
func (a *Authorization) Wait(ctx context.Context) (Account, error) {
	defer a.Cancel()
	var res codeResult
	select {
	case res = <-a.codes:
	case <-ctx.Done():
		return Account{}, ctx.Err()
	case <-a.ctx.Done():
		return Account{}, context.Canceled
	}
	if res.err != nil {
		return Account{}, res.err
	}
	xctx, cancel := context.WithTimeout(bgContext(), 60*time.Second)
	defer cancel()
	tok, err := a.cfg.Exchange(xctx, res.code, oauth2.VerifierOption(a.verifier))
	if err != nil {
		return Account{}, fmt.Errorf("exchanging the authorization code: %w", err)
	}
	if tok.RefreshToken == "" {
		return Account{}, fmt.Errorf("%s granted no lasting access (no refresh token): try again", a.p.info.Name)
	}

	acc := Account{Provider: a.p.info.ID, ID: a.accountID, ClientID: a.clientID, ClientSecret: a.cfg.ClientSecret}
	if acc.ClientID == "" {
		acc.ClientSecret = ""
	}
	if acc.ID == "" {
		acc.ID = newAccountID()
	}
	if err := saveToken(acc.ID, tok); err != nil {
		return Account{}, fmt.Errorf("saving the account's token: %w", err)
	}
	b, _, err := a.p.open(acc, a.cfg, tok)
	if err != nil {
		return Account{}, err
	}
	actx, cancel2 := context.WithTimeout(ctx, 60*time.Second)
	defer cancel2()
	if acc.User, err = b.account(actx); err != nil {
		return Account{}, fmt.Errorf("reading the account's details: %w", err)
	}
	return acc, nil
}

// Cancel abandons the authorization, stopping the loopback server.
func (a *Authorization) Cancel() {
	a.once.Do(func() {
		a.cancel()
		if a.srv != nil {
			a.srv.Close()
		}
		for _, ln := range a.ln {
			ln.Close()
		}
	})
}
