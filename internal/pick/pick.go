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

// Package pick is the conversation between shfm's file chooser portal
// backend (internal/portal) and the shfm it runs in a terminal to let the
// user choose (`shfm --pick SOCKET`): the backend listens on a Unix
// socket, the picker connects and reads one Request, and answers with one
// Reply — or closes the connection, which means "cancelled". The backend
// closing its end first (the application withdrew the request) makes the
// picker quit.
//
// Each message is one line of JSON.
package pick

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"time"

	"shfm/internal/opener"
)

// Mode is what the application asked for.
type Mode string

const (
	// ModeOpen chooses existing files, or folders if Request.Directory.
	ModeOpen Mode = "open"
	// ModeSave chooses a file name to save to, new or existing.
	ModeSave Mode = "save"
	// ModeSaveFiles chooses the folder Request.Files are saved in.
	ModeSaveFiles Mode = "save-files"
)

// Pattern kinds, as the portal's filters give them.
const (
	PatternGlob uint32 = 0
	PatternMIME uint32 = 1
)

// Pattern is one pattern of a Filter: a shell glob on the file name
// ("*.pdf") or a MIME type, possibly with a wildcard subtype ("image/*").
type Pattern struct {
	Kind    uint32 `json:"kind"`
	Pattern string `json:"pattern"`
}

// Filter is a named set of patterns ("Images", "PDF documents"): a file is
// listed if it matches any of them.
type Filter struct {
	Name     string    `json:"name"`
	Patterns []Pattern `json:"patterns"`
}

// ChoiceOption is one value of a multiple-choice Choice.
type ChoiceOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// Choice is an extra option the application shows in its dialog: a
// checkbox when Options is empty (values "true"/"false"), a combo box
// otherwise.
type Choice struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	Options []ChoiceOption `json:"options,omitempty"`
	Default string         `json:"default"`
}

// Request is what the application asked for.
type Request struct {
	Mode        Mode   `json:"mode"`
	Title       string `json:"title,omitempty"`
	AcceptLabel string `json:"accept_label,omitempty"`
	// Multiple allows choosing several entries (ModeOpen only).
	Multiple bool `json:"multiple,omitempty"`
	// Directory chooses folders instead of files (ModeOpen only).
	Directory bool `json:"directory,omitempty"`
	// CurrentFolder is where to start; empty: the picker's default.
	CurrentFolder string `json:"current_folder,omitempty"`
	// CurrentName is the suggested file name (ModeSave).
	CurrentName string `json:"current_name,omitempty"`
	// CurrentFile is the file being saved again, when there's one
	// (ModeSave): it gives both the folder and the name to start with.
	CurrentFile string `json:"current_file,omitempty"`
	// Files are the names of the files to save (ModeSaveFiles).
	Files   []string `json:"files,omitempty"`
	Filters []Filter `json:"filters,omitempty"`
	// CurrentFilter indexes Filters; -1 for none (everything listed).
	CurrentFilter int      `json:"current_filter"`
	Choices       []Choice `json:"choices,omitempty"`
}

// Reply is the user's choice.
type Reply struct {
	// Paths are absolute local paths: the chosen entries (ModeOpen), the
	// file to save to (ModeSave), or where each of Request.Files goes, in
	// the same order (ModeSaveFiles).
	Paths []string `json:"paths"`
	// Filter is the index in Request.Filters of the filter in use when
	// the user chose, -1 for none.
	Filter int `json:"filter"`
	// Choices has the value of every Request.Choices, by ID.
	Choices map[string]string `json:"choices,omitempty"`
}

// Match reports whether a file named name passes f, its type told by its
// name.
func (f Filter) Match(name string) bool {
	return f.MatchFile(name, nil)
}

// MatchFile is Match, a file whose name doesn't tell its type recognized
// by its content when readHead is set (see opener.DetectMimeType) — as GTK
// does on the local filesystem, not on network ones, where reading every
// file listed would cost too much.
func (f Filter) MatchFile(name string, readHead func(n int) ([]byte, error)) bool {
	lower := strings.ToLower(name)
	var mimeType string
	for _, p := range f.Patterns {
		switch p.Kind {
		case PatternGlob:
			// Globs are matched case-insensitively too: applications
			// rarely spell out every case ("*.[jJ][pP][gG]"), while
			// files often come in upper case from cameras and Windows.
			if ok, _ := filepath.Match(p.Pattern, name); ok {
				return true
			}
			if ok, _ := filepath.Match(strings.ToLower(p.Pattern), lower); ok {
				return true
			}
		case PatternMIME:
			if mimeType == "" {
				if readHead != nil {
					mimeType = opener.DetectMimeType(name, readHead)
				} else {
					mimeType = opener.MimeType(name)
				}
			}
			if opener.MimeTypeIs(mimeType, p.Pattern) {
				return true
			}
		}
	}
	return false
}

// DefaultChoices returns every choice's default value, by ID; a checkbox
// without one defaults to "false", a combo box to its first option.
func DefaultChoices(choices []Choice) map[string]string {
	out := make(map[string]string, len(choices))
	for _, c := range choices {
		v := c.Default
		if v == "" {
			if len(c.Options) > 0 {
				v = c.Options[0].ID
			} else {
				v = "false"
			}
		}
		out[c.ID] = v
	}
	return out
}

// ErrCancelled is Wait's error when the picker closed the connection
// without answering: the user cancelled.
var ErrCancelled = errors.New("cancelled")

// Session is the picker's end of the conversation.
type Session struct {
	conn net.Conn
	r    *bufio.Reader
}

// Dial connects to the backend at socketPath and reads its Request.
func Dial(socketPath string) (*Session, Request, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, Request{}, err
	}
	s := &Session{conn: conn, r: bufio.NewReader(conn)}
	var req Request
	if err := readLine(s.r, &req); err != nil {
		conn.Close()
		return nil, Request{}, err
	}
	return s, req, nil
}

// WatchWithdrawn calls fn, from another goroutine, once the backend closes
// its end: the application withdrew the request. The backend sends
// nothing after the Request, so anything read here means the same.
func (s *Session) WatchWithdrawn(fn func()) {
	go func() {
		var buf [64]byte
		for {
			if _, err := s.r.Read(buf[:]); err != nil {
				fn()
				return
			}
		}
	}()
}

// Send answers with the user's choice.
func (s *Session) Send(r Reply) error {
	return writeLine(s.conn, r)
}

// Close ends the conversation: without a Send first, it's a cancel.
func (s *Session) Close() error { return s.conn.Close() }

// Serve is the backend's end: it sends req on conn and waits for the
// Reply. It returns ErrCancelled if the picker closes the connection
// without one, and returns early (with the connection closed) if cancel
// is closed first.
func Serve(conn net.Conn, req Request, cancel <-chan struct{}) (Reply, error) {
	defer conn.Close()
	if err := writeLine(conn, req); err != nil {
		return Reply{}, err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-cancel:
			conn.Close()
		case <-done:
		}
	}()
	var reply Reply
	if err := readLine(bufio.NewReader(conn), &reply); err != nil {
		return Reply{}, ErrCancelled
	}
	return reply, nil
}

func writeLine(conn net.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

func readLine(r *bufio.Reader, v any) error {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v)
}
