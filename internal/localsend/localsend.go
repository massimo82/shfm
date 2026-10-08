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

// Package localsend sends files to, and receives them from, the devices
// on the local network running LocalSend (https://localsend.org) — phones
// and computers alike — with no server or account involved. It is only
// built in with the "localsend" build tag; without it, Available is false
// and Start fails with ErrUnavailable.
//
// A Service, started by the UI, announces the device (UDP multicast,
// falling back to asking every address of the local networks over HTTP),
// keeps the list of the devices found, and runs the HTTPS server the
// devices send to. The protocol itself is in protocol/ and its versions'
// packages (protocol/lsv2): this package only decides, with the user,
// what to send and receive, and reads and writes the files through
// internal/vfs — so files are sent from, and received into, any source.
package localsend

import (
	"errors"
	"fmt"
)

// Available reports whether LocalSend is built in.
var Available bool

// ErrUnavailable: shfm was built without the "localsend" tag.
var ErrUnavailable = errors.New("LocalSend not built in (tag localsend)")

// Settings are the user's choices.
type Settings struct {
	// Alias is the name other devices see, "" for the host name.
	Alias string
	// Port is the HTTP server's (and the announcements'), 0 for the
	// default (53317). Taken, another one is used.
	Port int
	// Receive: other devices may send files (each transfer is still
	// asked to the user).
	Receive bool
	// PIN, if set, must be given by senders.
	PIN string
}

// Device is a device found on the network.
type Device struct {
	Alias       string
	Model       string // may be empty
	Type        string // mobile, desktop, web, headless, server, or empty
	Fingerprint string
	IP          string
	Port        int
	HTTPS       bool
	Version     string // protocol version
}

// Label is how the device is shown: its alias, and its model if known.
func (d Device) Label() string {
	if d.Model != "" {
		return fmt.Sprintf("%s (%s)", d.Alias, d.Model)
	}
	return d.Alias
}

// IncomingFile is a file offered by a sender.
type IncomingFile struct {
	Name string // a path, for a file inside a folder ("Photos/a.jpg")
	Size int64
}

// Request is a transfer another device asks to make: the user accepts
// it (Accept) or declines it (Decline). A text message (IsMessage)
// needs no answer: it was received whole.
type Request struct {
	From  Device
	Files []IncomingFile
	Size  int64 // of all the files

	IsMessage bool
	Message   string

	r *request // nil for a message
}

// Events are how a Service reports to the UI; each is called from the
// Service's goroutines and must not block.
type Events struct {
	// Changed: the device list, or the Service's state, changed.
	Changed func()
	// Incoming: a request to answer, or a message to show.
	Incoming func(*Request)
	// Withdrawn: the sender gave up a request before it was answered.
	Withdrawn func(*Request)
}

// PINAsker asks the user the PIN dev wants (again, if wrong), false if
// the user gave up.
type PINAsker func(dev Device, wrong bool) (string, bool)

// Status describes a running Service.
type Status struct {
	Alias       string
	Port        int
	Fingerprint string
	Receive     bool
	PIN         bool // a PIN is set
	// Multicast is the error that stops announcements over multicast,
	// nil when they work (devices are then only found by scanning).
	Multicast error
}
