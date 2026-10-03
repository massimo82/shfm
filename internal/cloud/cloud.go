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

// Package cloud exposes cloud storage services — Google Drive, Dropbox and
// Microsoft OneDrive — as shfm sources, through the common vfs.FileSystem
// interface, so that every file operation, the FUSE mount and mirrors work
// on them as on any other source.
//
// It's an optional module, built only with the "cloud" build tag (like
// semantic search with "semantic"): without it, Available is false, no
// provider is listed and the UI shows no cloud section. The services are
// reached through their public REST APIs — Google Drive v3, Microsoft
// Graph, and Dropbox's through its official Go SDK — authorized with OAuth
// 2.0 (authorization code with PKCE, redirected to a loopback address).
//
// Design borrowed from rclone (MIT license, https://rclone.org): retrying
// rate-limited requests with backoff, caching Google Drive's folder IDs by
// path, and the loopback OAuth flow; the code is shfm's own.
package cloud

import "errors"

// Available reports whether shfm was built with the cloud module (the
// "cloud" build tag).
var Available bool

// Provider identifiers, as saved in config.RemoteSource.Kind and used as
// the scheme of the sources' labels ("gdrive://user@example.com").
const (
	GoogleDrive = "gdrive"
	Dropbox     = "dropbox"
	OneDrive    = "onedrive"
)

// ProviderInfo describes a cloud storage service.
type ProviderInfo struct {
	ID   string // GoogleDrive, Dropbox or OneDrive
	Name string // shown in the UI: "Google Drive", "Dropbox", "Microsoft OneDrive"

	// DefaultClientID is the OAuth client built into this shfm binary
	// (set at build time, see README "Cloud storage"); empty when the user
	// must register one of their own.
	DefaultClientID string

	// NeedsSecret tells whether the service's OAuth clients come with a
	// client secret that must be sent along (Google's "Desktop app"
	// clients do, though it isn't really secret in an installed
	// application); for the others it's optional.
	NeedsSecret bool

	// RedirectURI is the redirect URI to register with the OAuth client:
	// Google and Microsoft accept any port on a loopback address, Dropbox
	// wants the exact URI, port included.
	RedirectURI string
}

// Account is an authorized cloud storage account.
type Account struct {
	Provider     string // GoogleDrive, Dropbox or OneDrive
	ID           string // identifies the account's token in the token store
	User         string // the account's e-mail address (or user name)
	ClientID     string // the OAuth client it was authorized with
	ClientSecret string
}

// Label is the label of the account's source ("gdrive://user@example.com"):
// what the UI shows, and the prefix of its files' URIs.
func Label(provider, user string) string {
	return provider + "://" + user
}

var (
	// ErrUnavailable is returned by every operation in a build without
	// the cloud module.
	ErrUnavailable = errors.New("shfm was built without cloud storage support (build tag \"cloud\")")

	// ErrAuthorization means the account's authorization is no longer
	// valid (revoked, expired, or its token lost): it must be authorized
	// again.
	ErrAuthorization = errors.New("the account's authorization is no longer valid: authorize it again")
)
