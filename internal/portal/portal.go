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

// Package portal is a backend of xdg-desktop-portal's file chooser
// (org.freedesktop.impl.portal.FileChooser): with it configured,
// applications that ask the portal for a file dialog — Firefox and
// Chromium when choosing where to download, what to upload, or where to
// save a page, and every sandboxed application — get shfm instead, run in
// a new terminal window as `shfm --pick SOCKET` (see internal/pick for
// the conversation between the two).
package portal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"

	"shfm/internal/applog"
	"shfm/internal/idle"
	"shfm/internal/pick"
)

const (
	// BusName is the name the backend owns, as shfm.portal declares it.
	BusName      = "org.freedesktop.impl.portal.desktop.shfm"
	objectPath   = "/org/freedesktop/portal/desktop"
	iface        = "org.freedesktop.impl.portal.FileChooser"
	requestIface = "org.freedesktop.impl.portal.Request"

	// connectTimeout is how long the picker has to connect back once its
	// terminal is started.
	connectTimeout = time.Minute
)

// Portal responses.
const (
	responseSuccess   uint32 = 0
	responseCancelled uint32 = 1
	responseOther     uint32 = 2
)

// Picker runs `shfm --pick socketPath` in a new terminal window; exited
// delivers the terminal process's exit (which may come right away, see
// termlaunch.Start).
type Picker func(socketPath string) (exited <-chan error, err error)

type service struct {
	conn   *dbus.Conn
	picker Picker
	idle   *idle.Tracker
	// shutdown is closed when the service ends (terminated, or the bus
	// gone): requests in progress are withdrawn, as by their Close.
	shutdown chan struct{}
}

// Serve provides the backend on the session bus until it has gone unused
// for idleExit, then gives the name back and returns: the bus starts shfm
// again (see the D-Bus service file in contrib/) on the next request.
func Serve(picker Picker, idleExit time.Duration) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	s := &service{conn: conn, picker: picker, idle: idle.New(), shutdown: make(chan struct{})}
	if err := conn.ExportMethodTable(map[string]any{
		"OpenFile":  s.openFile,
		"SaveFile":  s.saveFile,
		"SaveFiles": s.saveFiles,
	}, objectPath, iface); err != nil {
		return err
	}
	reply, err := conn.RequestName(BusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("%s is already running", BusName)
	}
	// Ended by the session too: SIGTERM at logout, or the bus going away.
	sig, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stopSignals()
	stop := make(chan struct{})
	go func() {
		select {
		case <-sig.Done():
		case <-conn.Context().Done():
		}
		close(stop)
	}()
	s.idle.Wait(idleExit, 10*time.Second, stop)
	// Withdraw what's still open, letting each request clean up (its
	// socket folder) and close its picker, briefly.
	close(s.shutdown)
	grace := make(chan struct{})
	time.AfterFunc(5*time.Second, func() { close(grace) })
	s.idle.Wait(0, 50*time.Millisecond, grace)
	conn.ReleaseName(BusName)
	return nil
}

type results = map[string]dbus.Variant

func (s *service) openFile(handle dbus.ObjectPath, appID, parentWindow, title string, options results) (uint32, results, *dbus.Error) {
	req := parseOptions(pick.ModeOpen, title, options)
	return s.run(handle, req)
}

func (s *service) saveFile(handle dbus.ObjectPath, appID, parentWindow, title string, options results) (uint32, results, *dbus.Error) {
	req := parseOptions(pick.ModeSave, title, options)
	return s.run(handle, req)
}

func (s *service) saveFiles(handle dbus.ObjectPath, appID, parentWindow, title string, options results) (uint32, results, *dbus.Error) {
	req := parseOptions(pick.ModeSaveFiles, title, options)
	return s.run(handle, req)
}

// run shows the picker for req and waits for the user's choice. The
// application can withdraw the request meanwhile, calling Close on the
// Request object at handle.
func (s *service) run(handle dbus.ObjectPath, req pick.Request) (uint32, results, *dbus.Error) {
	defer s.idle.Begin()()

	cancel := make(chan struct{})
	var once sync.Once
	withdraw := func() { once.Do(func() { close(cancel) }) }
	if err := s.conn.ExportMethodTable(map[string]any{
		"Close": func() *dbus.Error {
			withdraw()
			return nil
		},
	}, handle, requestIface); err != nil {
		return responseOther, results{}, dbus.MakeFailedError(err)
	}
	defer s.conn.Export(nil, handle, requestIface)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-s.shutdown:
			withdraw()
		case <-done:
		}
	}()

	reply, err := s.pick(req, cancel)
	switch {
	case errors.Is(err, pick.ErrCancelled):
		return responseCancelled, results{}, nil
	case err != nil:
		applog.Warn("file chooser failed", "error", err)
		return responseOther, results{}, nil
	}
	return responseSuccess, replyResults(req, reply), nil
}

// pick starts the picker and holds the conversation with it.
func (s *service) pick(req pick.Request, cancel <-chan struct{}) (pick.Reply, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	// A Unix socket's path must fit in 108 bytes: MkdirTemp adds ~20.
	if base == "" || len(base) > 80 {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "shfm-pick-") // private: mode 0700
	if err != nil {
		return pick.Reply{}, err
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return pick.Reply{}, err
	}
	defer ln.Close()

	exited, err := s.picker(sock)
	if err != nil {
		return pick.Reply{}, err
	}

	type accepted struct {
		conn net.Conn
		err  error
	}
	acc := make(chan accepted, 1)
	go func() {
		c, err := ln.Accept()
		acc <- accepted{c, err}
	}()
	// Returning before the picker connected closes the listener: a
	// connection accepted meanwhile must still be closed, or that picker
	// would never learn the request is gone.
	abandon := func() {
		go func() {
			if a := <-acc; a.conn != nil {
				a.conn.Close()
			}
		}()
	}
	timeout := time.NewTimer(connectTimeout)
	defer timeout.Stop()
	for {
		select {
		case a := <-acc:
			if a.err != nil {
				return pick.Reply{}, a.err
			}
			// The socket's done its job: don't leave it behind should
			// this process be killed while the user is choosing.
			ln.Close()
			os.RemoveAll(dir)
			return pick.Serve(a.conn, req, cancel)
		case err := <-exited:
			// A terminal that failed never runs the picker; one that
			// exited fine may have handed the window to a running
			// instance: keep waiting for that one.
			if err != nil {
				abandon()
				return pick.Reply{}, fmt.Errorf("the terminal failed: %w", err)
			}
			exited = nil
		case <-timeout.C:
			abandon()
			return pick.Reply{}, errors.New("the picker did not start")
		case <-cancel:
			abandon()
			return pick.Reply{}, pick.ErrCancelled
		}
	}
}

// Wire types of the portal's filters and choices.
type patternTuple struct {
	Kind    uint32
	Pattern string
}

type filterTuple struct {
	Name     string
	Patterns []patternTuple
}

type choiceOptionTuple struct {
	ID    string
	Label string
}

type choiceTuple struct {
	ID      string
	Label   string
	Options []choiceOptionTuple
	Default string
}

type choiceValueTuple struct {
	ID    string
	Value string
}

// parseOptions turns the method's options into a pick.Request; malformed
// options are ignored, as if absent.
func parseOptions(mode pick.Mode, title string, options results) pick.Request {
	req := pick.Request{Mode: mode, Title: title, CurrentFilter: -1}
	str := func(key string) string {
		var v string
		if o, ok := options[key]; ok {
			o.Store(&v)
		}
		return v
	}
	boolean := func(key string) bool {
		var v bool
		if o, ok := options[key]; ok {
			o.Store(&v)
		}
		return v
	}
	path := func(key string) string {
		var v []byte
		if o, ok := options[key]; ok {
			o.Store(&v)
		}
		return string(bytes.TrimRight(v, "\x00"))
	}

	req.AcceptLabel = str("accept_label")
	if mode == pick.ModeOpen {
		req.Multiple = boolean("multiple")
		req.Directory = boolean("directory")
	}
	req.CurrentFolder = path("current_folder")
	if mode == pick.ModeSave {
		req.CurrentName = str("current_name")
		req.CurrentFile = path("current_file")
	}
	if mode == pick.ModeSaveFiles {
		var files [][]byte
		if o, ok := options["files"]; ok {
			o.Store(&files)
		}
		for _, f := range files {
			req.Files = append(req.Files, string(bytes.TrimRight(f, "\x00")))
		}
	}

	if mode != pick.ModeSaveFiles {
		var filters []filterTuple
		if o, ok := options["filters"]; ok {
			o.Store(&filters)
		}
		for _, f := range filters {
			req.Filters = append(req.Filters, toFilter(f))
		}
		var current filterTuple
		if o, ok := options["current_filter"]; ok && o.Store(&current) == nil {
			// It should be one of filters; given without them, it
			// applies alone.
			req.CurrentFilter = indexOfFilter(req.Filters, toFilter(current))
			if req.CurrentFilter < 0 {
				req.Filters = append(req.Filters, toFilter(current))
				req.CurrentFilter = len(req.Filters) - 1
			}
		}
	}

	var choices []choiceTuple
	if o, ok := options["choices"]; ok {
		o.Store(&choices)
	}
	for _, c := range choices {
		ch := pick.Choice{ID: c.ID, Label: c.Label, Default: c.Default}
		for _, opt := range c.Options {
			ch.Options = append(ch.Options, pick.ChoiceOption{ID: opt.ID, Label: opt.Label})
		}
		req.Choices = append(req.Choices, ch)
	}
	return req
}

func toFilter(f filterTuple) pick.Filter {
	out := pick.Filter{Name: f.Name}
	for _, p := range f.Patterns {
		out.Patterns = append(out.Patterns, pick.Pattern{Kind: p.Kind, Pattern: p.Pattern})
	}
	return out
}

func indexOfFilter(filters []pick.Filter, f pick.Filter) int {
	for i, g := range filters {
		if g.Name != f.Name || len(g.Patterns) != len(f.Patterns) {
			continue
		}
		same := true
		for j := range g.Patterns {
			same = same && g.Patterns[j] == f.Patterns[j]
		}
		if same {
			return i
		}
	}
	return -1
}

// replyResults turns the user's choice into the method's results.
func replyResults(req pick.Request, reply pick.Reply) results {
	uris := make([]string, 0, len(reply.Paths))
	for _, p := range reply.Paths {
		uris = append(uris, (&url.URL{Scheme: "file", Path: p}).String())
	}
	out := results{"uris": dbus.MakeVariant(uris)}
	if reply.Filter >= 0 && reply.Filter < len(req.Filters) {
		f := req.Filters[reply.Filter]
		t := filterTuple{Name: f.Name, Patterns: []patternTuple{}}
		for _, p := range f.Patterns {
			t.Patterns = append(t.Patterns, patternTuple{Kind: p.Kind, Pattern: p.Pattern})
		}
		out["current_filter"] = dbus.MakeVariant(t)
	}
	if len(req.Choices) > 0 {
		values := make([]choiceValueTuple, 0, len(req.Choices))
		for _, c := range req.Choices {
			values = append(values, choiceValueTuple{ID: c.ID, Value: reply.Choices[c.ID]})
		}
		out["choices"] = dbus.MakeVariant(values)
	}
	return out
}
