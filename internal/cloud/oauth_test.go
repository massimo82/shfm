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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeTokenServer is an OAuth token endpoint: it exchanges "good-code"
// (sent with a PKCE verifier) for refresh token "r1", and refreshes "r1"
// by rotating it to "r2"; any other refresh token is revoked.
type fakeTokenServer struct {
	srv *httptest.Server

	mu        sync.Mutex
	refreshes int
}

func newFakeTokenServer(t *testing.T) *fakeTokenServer {
	f := &fakeTokenServer{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		fail := func(code string) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": code})
		}
		if r.Form.Get("client_id") != "cid" {
			fail("invalid_client")
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
				fail("invalid_grant")
				return
			}
			writeJSON(w, 200, map[string]any{"access_token": testToken, "token_type": "Bearer", "refresh_token": "r1", "expires_in": 3600})
		case "refresh_token":
			f.mu.Lock()
			f.refreshes++
			f.mu.Unlock()
			if r.Form.Get("refresh_token") != "r1" {
				fail("invalid_grant")
				return
			}
			writeJSON(w, 200, map[string]any{"access_token": testToken, "token_type": "Bearer", "refresh_token": "r2", "expires_in": 3600})
		default:
			fail("unsupported_grant_type")
		}
	}))
	t.Cleanup(f.srv.Close)
	old := driveEndpoint
	driveEndpoint = oauth2.Endpoint{AuthURL: f.srv.URL + "/auth", TokenURL: f.srv.URL + "/token", AuthStyle: oauth2.AuthStyleInParams}
	t.Cleanup(func() { driveEndpoint = old })
	return f
}

func startDriveAuth(t *testing.T) (*Authorization, url.Values) {
	t.Helper()
	a, err := StartAuthorization(GoogleDrive, "cid", "secret", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Cancel)
	u, err := url.Parse(a.URL())
	if err != nil {
		t.Fatal(err)
	}
	return a, u.Query()
}

func waitAuth(t *testing.T, a *Authorization) (Account, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.Wait(ctx)
}

// TestAuthorizationLoopback: the browser's redirect to the loopback
// server completes the authorization, saving the account's token.
func TestAuthorizationLoopback(t *testing.T) {
	isolateConfig(t)
	newFakeTokenServer(t)
	newFakeDrive(t)
	a, q := startDriveAuth(t)

	if q.Get("client_id") != "cid" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || !strings.Contains(q.Get("scope"), "auth/drive") {
		t.Errorf("authorization URL parameters: %v", q)
	}
	redirect := q.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") {
		t.Fatalf("redirect_uri = %q", redirect)
	}

	// The browser, redirected back.
	resp, err := http.Get(redirect + "?code=good-code&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "authorized") {
		t.Errorf("redirect page: %s", page)
	}

	acc, err := waitAuth(t, a)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if acc.Provider != GoogleDrive || acc.User != "user@example.com" || acc.ID == "" || acc.ClientID != "cid" || acc.ClientSecret != "secret" {
		t.Errorf("account = %+v", acc)
	}
	tok, err := loadToken(acc.ID)
	if err != nil || tok.RefreshToken != "r1" {
		t.Errorf("saved token = %+v, %v", tok, err)
	}
	// The loopback server is gone once done.
	if _, err := http.Get(redirect); err == nil {
		t.Error("the loopback server still answers after the authorization")
	}

	fs, err := Dial(acc)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer fs.Close()
	if fs.Label() != "gdrive://user@example.com" {
		t.Errorf("Label = %q", fs.Label())
	}
	if _, err := fs.List("/"); err != nil {
		t.Errorf("List: %v", err)
	}
}

// TestAuthorizationPasted: the address the browser ended up on, or the
// bare code, pasted by the user (browser on another machine).
func TestAuthorizationPasted(t *testing.T) {
	isolateConfig(t)
	newFakeTokenServer(t)
	newFakeDrive(t)

	a, q := startDriveAuth(t)
	a.Submit("  http://127.0.0.1:1/?state=" + q.Get("state") + "&code=good-code&scope=x  ")
	if _, err := waitAuth(t, a); err != nil {
		t.Errorf("pasted address: %v", err)
	}

	a, _ = startDriveAuth(t)
	a.Submit("good-code")
	if _, err := waitAuth(t, a); err != nil {
		t.Errorf("pasted code: %v", err)
	}

	a, _ = startDriveAuth(t)
	a.Submit("http://127.0.0.1:1/?state=forged&code=good-code")
	if _, err := waitAuth(t, a); err == nil || !strings.Contains(err.Error(), "state") {
		t.Errorf("forged state: %v", err)
	}

	a, q = startDriveAuth(t)
	a.Submit("http://127.0.0.1:1/?error=access_denied&state=" + q.Get("state"))
	if _, err := waitAuth(t, a); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("refused: %v", err)
	}

	a, _ = startDriveAuth(t)
	a.Submit("wrong-code")
	if _, err := waitAuth(t, a); err == nil {
		t.Error("a wrong code was accepted")
	}

	a, _ = startDriveAuth(t)
	a.Cancel()
	if _, err := waitAuth(t, a); !errors.Is(err, context.Canceled) {
		t.Errorf("after Cancel: %v", err)
	}
}

// TestAuthorizationNeedsClient: without a client ID (none built in) or,
// for Google, without its secret, authorizing can't start.
func TestAuthorizationNeedsClient(t *testing.T) {
	if googleClientID == "" {
		if _, err := StartAuthorization(GoogleDrive, "", "", ""); err == nil {
			t.Error("started without a client ID")
		}
	}
	if _, err := StartAuthorization(GoogleDrive, "cid", "", ""); err == nil {
		t.Error("started a Google authorization without the client secret")
	}
	if _, err := StartAuthorization("nope", "cid", "s", ""); err == nil {
		t.Error("started an authorization for an unknown service")
	}
}

// TestTokenRefresh: an expired token is refreshed, and the rotated
// refresh token saved; a revoked one is ErrAuthorization.
func TestTokenRefresh(t *testing.T) {
	isolateConfig(t)
	ts := newFakeTokenServer(t)
	newFakeDrive(t)
	acc := Account{Provider: GoogleDrive, ID: "acc", User: "user@example.com", ClientID: "cid", ClientSecret: "secret"}

	saveToken("acc", &oauth2.Token{AccessToken: "stale", RefreshToken: "r1", Expiry: time.Now().Add(-time.Hour)})
	fs, err := Dial(acc)
	if err != nil {
		t.Fatalf("Dial with an expired token: %v", err)
	}
	fs.Close()
	if ts.refreshes != 1 {
		t.Errorf("%d refreshes, want 1", ts.refreshes)
	}
	if tok, _ := loadToken("acc"); tok == nil || tok.RefreshToken != "r2" || tok.AccessToken != testToken {
		t.Errorf("saved token after the refresh: %+v", tok)
	}

	saveToken("acc", &oauth2.Token{AccessToken: "stale", RefreshToken: "revoked", Expiry: time.Now().Add(-time.Hour)})
	if _, err := Dial(acc); !errors.Is(err, ErrAuthorization) {
		t.Errorf("Dial with a revoked token: %v, want ErrAuthorization", err)
	}
	if _, err := Dial(Account{Provider: GoogleDrive, ID: "missing", ClientID: "cid"}); !errors.Is(err, ErrAuthorization) {
		t.Errorf("Dial without a saved token: %v, want ErrAuthorization", err)
	}
}

// TestProviders: the services, in the UI's order, with their names.
func TestProviders(t *testing.T) {
	var got []string
	for _, p := range Providers() {
		got = append(got, p.ID+"="+p.Name)
	}
	if want := "gdrive=Google Drive,dropbox=Dropbox,onedrive=Microsoft OneDrive"; strings.Join(got, ",") != want {
		t.Errorf("Providers = %v, want %s", got, want)
	}
	if !Available {
		t.Error("Available is false in a cloud build")
	}
}
