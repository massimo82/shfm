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

//go:build linux

package polkitagent

import (
	"bufio"
	"context"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

func TestUnescape(t *testing.T) {
	cases := map[string]string{
		`Password: `:        "Password: ",
		`Pa\303\250ssword:`: "Paèssword:",
		`a\\b \"q\" \t\n`:   "a\\b \"q\" \t\n",
		`trailing\`:         `trailing\`,
		`\1\12x`:            "\x01\nx",
	}
	for in, want := range cases {
		if got := unescape(in); got != want {
			t.Errorf("unescape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseStartTime(t *testing.T) {
	stat := "1234 (a b) c) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 987654 20 21"
	got, err := parseStartTime(stat)
	if err != nil || got != 987654 {
		t.Fatalf("parseStartTime = %d, %v; want 987654", got, err)
	}
	if _, err := parseStartTime("garbage"); err == nil {
		t.Error("expected an error for a malformed line")
	}
}

func TestPickUserPrefersCurrentUser(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	ids := []identity{
		{Kind: "unix-group", Details: map[string]dbus.Variant{"gid": dbus.MakeVariant(uint32(0))}},
		{Kind: "unix-user", Details: map[string]dbus.Variant{"uid": dbus.MakeVariant(uint32(0))}},
		{Kind: "unix-user", Details: map[string]dbus.Variant{"uid": dbus.MakeVariant(uint32(os.Getuid()))}},
	}
	if got, err := pickUser(ids); err != nil || got != me.Username {
		t.Errorf("pickUser = %q, %v; want %q", got, err, me.Username)
	}
	if _, err := pickUser(ids[:1]); err == nil {
		t.Error("expected an error without any unix-user identity")
	}
}

// fakeHelper serves one connection on a temporary helper socket the way
// polkit-agent-helper-1 does: it reads user and cookie, asks for the
// password (after an info line), and answers SUCCESS only for "secret".
func fakeHelper(t *testing.T) (got chan []string) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "helper.socket")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	saved := helperSocket
	helperSocket = sock
	t.Cleanup(func() { helperSocket = saved })

	got = make(chan []string, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			r := bufio.NewReader(c)
			name, _ := r.ReadString('\n')
			cookie, _ := r.ReadString('\n')
			c.Write([]byte("PAM_TEXT_INFO Caps\\040Lock is on\nPAM_PROMPT_ECHO_OFF Password: \n"))
			pw, _ := r.ReadString('\n')
			got <- []string{strings.TrimSpace(name), strings.TrimSpace(cookie), strings.TrimSuffix(pw, "\n")}
			if pw == "secret\n" {
				c.Write([]byte("SUCCESS\n"))
			} else {
				c.Write([]byte("FAILURE\n"))
			}
			c.Close()
		}
	}()
	return got
}

func TestSessionSpeaksHelperProtocol(t *testing.T) {
	got := fakeHelper(t)
	var asked Prompt
	a := &Agent{prompt: func(ctx context.Context, p Prompt) (string, bool) {
		asked = p
		return "secret", true
	}}
	var notice string
	ok, err := a.session(context.Background(), "Run chown", "alice", "cookie-1", &notice)
	if !ok || err != nil {
		t.Fatalf("session = %v, %v; want success", ok, err)
	}
	if g := <-got; g[0] != "alice" || g[1] != "cookie-1" || g[2] != "secret" {
		t.Errorf("helper received %q", g)
	}
	if asked.Text != "Password: " || asked.Echo || asked.Notice != "Caps Lock is on" || asked.User != "alice" || asked.Message != "Run chown" {
		t.Errorf("prompt = %+v", asked)
	}
}

func TestBeginRetriesThenGivesUp(t *testing.T) {
	fakeHelper(t)
	var notices []string
	a := &Agent{cancels: map[string]context.CancelFunc{}, prompt: func(ctx context.Context, p Prompt) (string, bool) {
		notices = append(notices, p.Notice)
		return "wrong", true
	}}
	me := uint32(os.Getuid())
	ids := []identity{{Kind: "unix-user", Details: map[string]dbus.Variant{"uid": dbus.MakeVariant(me)}}}
	if err := a.begin("act", "msg", "", nil, "c", ids); err != nil {
		t.Fatalf("a failed authentication must still end the request normally, got %v", err)
	}
	if len(notices) != maxAttempts {
		t.Fatalf("asked %d times, want %d", len(notices), maxAttempts)
	}
	if !strings.Contains(notices[1], "try again") {
		t.Errorf("second prompt should say the first attempt failed, got %q", notices[1])
	}
}

func TestBeginDismissed(t *testing.T) {
	fakeHelper(t)
	a := &Agent{cancels: map[string]context.CancelFunc{}, prompt: func(ctx context.Context, p Prompt) (string, bool) {
		return "", false
	}}
	ids := []identity{{Kind: "unix-user", Details: map[string]dbus.Variant{"uid": dbus.MakeVariant(uint32(os.Getuid()))}}}
	err := a.begin("act", "msg", "", nil, "c", ids)
	if err == nil || err.Name != errCancelled {
		t.Fatalf("dismissing must answer %s, got %v", errCancelled, err)
	}
}
