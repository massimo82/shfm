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

//go:build !localsend

package localsend

import (
	"shfm/internal/fileops"
	"shfm/internal/vfs"
)

func init() { Available = false }

type request struct{}

// Service is LocalSend running.
type Service struct{}

// Start starts LocalSend.
func Start(dir string, st Settings, ev Events) (*Service, error) { return nil, ErrUnavailable }

// Close stops the Service.
func (s *Service) Close() {}

// Status returns the Service's state.
func (s *Service) Status() Status { return Status{} }

// Update applies new settings.
func (s *Service) Update(st Settings) {}

// Scanning reports whether a scan for devices is going on.
func (s *Service) Scanning() bool { return false }

// Devices returns the devices found.
func (s *Service) Devices() []Device { return nil }

// Rescan looks for the devices again.
func (s *Service) Rescan() {}

// Send sends items to dev.
func (s *Service) Send(dev Device, items []fileops.Item, ask PINAsker, prog *fileops.Progress) *fileops.Result {
	return &fileops.Result{Errors: []error{ErrUnavailable}}
}

// SendText sends a text message to dev.
func (s *Service) SendText(dev Device, text string, ask PINAsker, prog *fileops.Progress) *fileops.Result {
	return &fileops.Result{Errors: []error{ErrUnavailable}}
}

// Decline refuses the request.
func (r *Request) Decline() {}

// Accept accepts the request.
func (r *Request) Accept(fs vfs.FileSystem, dir string, prog *fileops.Progress) *fileops.Result {
	return &fileops.Result{Errors: []error{ErrUnavailable}}
}
