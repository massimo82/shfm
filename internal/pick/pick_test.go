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

package pick

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFilterMatch(t *testing.T) {
	images := Filter{Name: "Images", Patterns: []Pattern{{PatternMIME, "image/*"}}}
	pdf := Filter{Name: "PDF", Patterns: []Pattern{{PatternGlob, "*.pdf"}}}
	for _, tc := range []struct {
		f    Filter
		name string
		want bool
	}{
		{images, "photo.png", true},
		{images, "photo.JPG", true},
		{images, "notes.txt", false},
		{pdf, "doc.pdf", true},
		{pdf, "DOC.PDF", true},
		{pdf, "doc.pdf.txt", false},
		{Filter{Patterns: []Pattern{{PatternGlob, "*"}}}, "anything", true},
		{Filter{}, "anything", false},
	} {
		if got := tc.f.Match(tc.name); got != tc.want {
			t.Errorf("%s.Match(%q) = %v, want %v", tc.f.Name, tc.name, got, tc.want)
		}
	}
}

func TestDefaultChoices(t *testing.T) {
	got := DefaultChoices([]Choice{
		{ID: "check"},
		{ID: "checked", Default: "true"},
		{ID: "combo", Options: []ChoiceOption{{"a", "A"}, {"b", "B"}}},
		{ID: "combo2", Options: []ChoiceOption{{"a", "A"}, {"b", "B"}}, Default: "b"},
	})
	want := map[string]string{"check": "false", "checked": "true", "combo": "a", "combo2": "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultChoices = %v, want %v", got, want)
	}
}

// listen starts a backend end on a socket in a temporary folder and returns
// its path and the first accepted connection.
func listen(t *testing.T) (string, <-chan net.Conn) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "socket")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	conns := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			conns <- c
		}
	}()
	return sock, conns
}

func TestConversation(t *testing.T) {
	sock, conns := listen(t)
	req := Request{Mode: ModeSave, Title: "Save", CurrentName: "a.txt", CurrentFilter: -1}
	want := Reply{Paths: []string{"/tmp/a.txt"}, Filter: -1}

	type result struct {
		reply Reply
		err   error
	}
	done := make(chan result, 1)
	go func() {
		r, err := Serve(<-conns, req, nil)
		done <- result{r, err}
	}()

	s, got, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, req) {
		t.Fatalf("request = %+v, want %+v", got, req)
	}
	if err := s.Send(want); err != nil {
		t.Fatal(err)
	}
	s.Close()
	r := <-done
	if r.err != nil || !reflect.DeepEqual(r.reply, want) {
		t.Fatalf("Serve = %+v, %v; want %+v", r.reply, r.err, want)
	}
}

func TestConversationCancelledByPicker(t *testing.T) {
	sock, conns := listen(t)
	done := make(chan error, 1)
	go func() {
		_, err := Serve(<-conns, Request{Mode: ModeOpen}, nil)
		done <- err
	}()
	s, _, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := <-done; !errors.Is(err, ErrCancelled) {
		t.Fatalf("Serve error = %v, want ErrCancelled", err)
	}
}

func TestConversationWithdrawnByBackend(t *testing.T) {
	sock, conns := listen(t)
	cancel := make(chan struct{})
	go func() { Serve(<-conns, Request{Mode: ModeOpen}, cancel) }()

	s, _, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	withdrawn := make(chan struct{})
	s.WatchWithdrawn(func() { close(withdrawn) })
	close(cancel)
	select {
	case <-withdrawn:
	case <-time.After(5 * time.Second):
		t.Fatal("the picker never learned the request was withdrawn")
	}
}

func TestFilterMatchFileByContent(t *testing.T) {
	// A sandboxed Shared MIME-info database, not the machine's.
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_DATA_DIRS", data)
	if err := os.MkdirAll(filepath.Join(data, "mime"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"globs2":     "50:application/pdf:*.pdf\n50:text/x-csrc:*.c\n",
		"magic":      "MIME-Magic\x00\n[50:application/pdf]\n>0=\x00\x04%PDF\n",
		"subclasses": "text/x-csrc text/plain\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(data, "mime", name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pdf := Filter{Name: "PDF", Patterns: []Pattern{{PatternMIME, "application/pdf"}}}
	text := Filter{Name: "Text", Patterns: []Pattern{{PatternMIME, "text/plain"}}}
	head := func(content string) func(int) ([]byte, error) {
		return func(n int) ([]byte, error) { return []byte(content)[:min(n, len(content))], nil }
	}
	for _, tc := range []struct {
		f        Filter
		name     string
		readHead func(int) ([]byte, error)
		want     bool
	}{
		{pdf, "scan", head("%PDF-1.7"), true},
		{pdf, "scan", nil, false}, // by name only
		{pdf, "notes", head("hello\n"), false},
		{text, "notes", head("hello\n"), true},
		{text, "main.c", nil, true}, // a subclass of text/plain
		{text, "scan", head("%PDF-1.7"), false},
	} {
		if got := tc.f.MatchFile(tc.name, tc.readHead); got != tc.want {
			t.Errorf("%s.MatchFile(%q, content %v) = %v, want %v", tc.f.Name, tc.name, tc.readHead != nil, got, tc.want)
		}
	}
}
