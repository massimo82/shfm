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

// Package filemanager1 implements the org.freedesktop.FileManager1 D-Bus
// interface — how applications ask the file manager to show a folder, or
// a file in its folder: browsers' "Show in folder" / "Open containing
// folder" on a download, among others. Each request opens shfm in a new
// terminal window (see internal/termlaunch) on what was asked.
//
// shfm provides it like any other file manager does (Thunar, Dolphin,
// Nautilus...), through a D-Bus service file (see contrib/): which one
// the bus starts is the system's business.
package filemanager1

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"shfm/internal/applog"
	"shfm/internal/idle"
)

const (
	busName    = "org.freedesktop.FileManager1"
	objectPath = "/org/freedesktop/FileManager1"
	iface      = "org.freedesktop.FileManager1"
)

// Launcher opens shfm, with the given command line arguments, in a new
// terminal window.
type Launcher func(args []string) error

// ErrNameTaken means another file manager already provides the service.
var ErrNameTaken = errors.New(busName + " is already provided by another program")

type service struct {
	launch Launcher
	idle   *idle.Tracker
}

// Serve provides the service on the session bus until it has gone unused
// for idleExit, then gives the name back and returns: the bus starts shfm
// again on the next request.
func Serve(launch Launcher, idleExit time.Duration) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	s := &service{launch: launch, idle: idle.New()}
	if err := conn.ExportMethodTable(map[string]any{
		"ShowFolders":        s.showFolders,
		"ShowItems":          s.showItems,
		"ShowItemProperties": s.showItemProperties,
	}, objectPath, iface); err != nil {
		return err
	}
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return ErrNameTaken
	}
	s.idle.Wait(idleExit, 10*time.Second, conn.Context().Done())
	conn.ReleaseName(busName)
	return nil
}

// ShowFolders opens each folder in its own window.
func (s *service) showFolders(uris []string, startupID string) *dbus.Error {
	defer s.idle.Begin()()
	return s.run(Folders(uris))
}

// ShowItems opens the folder of the items, with them selected: a window
// per folder when they're in different ones.
func (s *service) showItems(uris []string, startupID string) *dbus.Error {
	defer s.idle.Begin()()
	return s.run(Items(uris))
}

// ShowItemProperties opens each item's folder with its properties shown.
func (s *service) showItemProperties(uris []string, startupID string) *dbus.Error {
	defer s.idle.Begin()()
	return s.run(Properties(uris))
}

func (s *service) run(cmds [][]string, err error) *dbus.Error {
	errs := []error{err}
	for _, args := range cmds {
		errs = append(errs, s.launch(args))
	}
	if err := errors.Join(errs...); err != nil {
		applog.Warn("FileManager1 request failed", "error", err)
		return dbus.MakeFailedError(err)
	}
	return nil
}

// Folders returns shfm's arguments for each folder of ShowFolders.
func Folders(uris []string) ([][]string, error) {
	paths, err := localPaths(uris)
	var cmds [][]string
	for _, p := range paths {
		cmds = append(cmds, []string{"--", p})
	}
	return cmds, err
}

// Items returns shfm's arguments for ShowItems: one command per folder,
// in the order the folders first appear, with its items.
func Items(uris []string) ([][]string, error) {
	paths, err := localPaths(uris)
	var order []string
	byDir := map[string][]string{}
	for _, p := range paths {
		dir := filepath.Dir(p)
		if _, seen := byDir[dir]; !seen {
			order = append(order, dir)
		}
		byDir[dir] = append(byDir[dir], p)
	}
	var cmds [][]string
	for _, dir := range order {
		cmds = append(cmds, append([]string{"--select", "--"}, byDir[dir]...))
	}
	return cmds, err
}

// Properties returns shfm's arguments for each item of
// ShowItemProperties.
func Properties(uris []string) ([][]string, error) {
	paths, err := localPaths(uris)
	var cmds [][]string
	for _, p := range paths {
		cmds = append(cmds, []string{"--properties", "--", p})
	}
	return cmds, err
}

// localPaths converts file:// URIs (or plain absolute paths, which some
// applications send) to local paths; the others are reported in err,
// the valid ones returned anyway.
func localPaths(uris []string) ([]string, error) {
	var out []string
	var bad []string
	for _, u := range uris {
		if p, ok := LocalPath(u); ok {
			out = append(out, p)
		} else {
			bad = append(bad, u)
		}
	}
	if len(bad) > 0 {
		return out, fmt.Errorf("not a local file: %s", strings.Join(bad, ", "))
	}
	return out, nil
}

// LocalPath returns the local path a file:// URI, or an absolute path,
// names.
func LocalPath(s string) (string, bool) {
	if strings.HasPrefix(s, "/") {
		return filepath.Clean(s), true
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || !strings.HasPrefix(u.Path, "/") {
		return "", false
	}
	return filepath.Clean(u.Path), true
}
