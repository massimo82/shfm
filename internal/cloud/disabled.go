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

//go:build !cloud

package cloud

import (
	"context"

	"shfm/internal/vfs"
)

func init() { Available = false }

// Providers lists the supported services: none without the cloud module.
func Providers() []ProviderInfo { return nil }

// Provider returns the service with the given ID.
func Provider(id string) (ProviderInfo, bool) { return ProviderInfo{}, false }

// Dial opens acc as a source.
func Dial(acc Account) (vfs.FileSystem, error) { return nil, ErrUnavailable }

// Forget deletes an account's token.
func Forget(id string) error { return ErrUnavailable }

// Authorization is an OAuth authorization in progress.
type Authorization struct{}

// StartAuthorization starts authorizing an account of provider.
func StartAuthorization(provider, clientID, clientSecret, accountID string) (*Authorization, error) {
	return nil, ErrUnavailable
}

// URL is the page the user authorizes shfm on.
func (a *Authorization) URL() string { return "" }

// Submit hands over what the user pasted.
func (a *Authorization) Submit(pasted string) {}

// Wait waits for the authorization to complete.
func (a *Authorization) Wait(ctx context.Context) (Account, error) { return Account{}, ErrUnavailable }

// Cancel abandons the authorization.
func (a *Authorization) Cancel() {}
