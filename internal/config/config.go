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

// Package config manages the user's persistent preferences (layout, saved
// network sources) as JSON under $XDG_CONFIG_HOME.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"

	"shfm/internal/secret"
)

// RemoteSource is a network source (SMB, NFS or SFTP) saved by the user,
// with a freely chosen name to recognize it in the picker.
type RemoteSource struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"` // "smb" | "nfs" | "sftp"
	Host       string `json:"host"`
	Share      string `json:"share"`       // SMB share
	Export     string `json:"export"`      // NFS export path
	RemotePath string `json:"remote_path"` // SFTP starting path
	Port       int    `json:"port,omitempty"`
	Domain     string `json:"domain,omitempty"`
	User       string `json:"user,omitempty"`
	Guest      bool   `json:"guest,omitempty"`

	// EncryptedPassword holds the SMB or SFTP password encrypted at rest
	// (see package "secret"): saved only if the user entered one when the
	// source was saved. Never written to disk in plain text.
	EncryptedPassword string `json:"encrypted_password,omitempty"`
}

// DecryptedPassword decrypts EncryptedPassword, if present.
func (r RemoteSource) DecryptedPassword() (string, error) {
	return secret.Decrypt(r.EncryptedPassword)
}

// MirrorEndpoint identifies one end of a mirror independently of where,
// or whether, its source is mounted/connected right now.
type MirrorEndpoint struct {
	// Source is "uuid:<filesystem UUID>" for a local disk or removable
	// drive, "local" for a local path on a filesystem without a UUID, and
	// the source's label otherwise ("smb://host/share", "nfs://host/export",
	// "sftp://user@host", "mtp://<device id>").
	Source string `json:"source"`
	// Path is relative to the mount point for "uuid:" sources ("" = the
	// mount point itself), absolute within the source otherwise.
	Path string `json:"path"`
	// Label is how the endpoint looked when the mirror was created, shown
	// while the source isn't available.
	Label string `json:"label"`
}

// MirrorPair is a one-way mirror kept in sync automatically (see package
// mirror): Dst is made identical to Src, deletions included.
type MirrorPair struct {
	ID       string         `json:"id"`
	Src      MirrorEndpoint `json:"src"`
	Dst      MirrorEndpoint `json:"dst"`
	UseRsync bool           `json:"use_rsync,omitempty"` // only meaningful between local sources
	Paused   bool           `json:"paused,omitempty"`
}

// Config groups all persistent preferences.
type Config struct {
	DualPane      bool           `json:"dual_pane"`
	ShowHidden    bool           `json:"show_hidden"`
	RemoteSources []RemoteSource `json:"remote_sources"`
	MirrorPairs   []MirrorPair   `json:"mirror_pairs,omitempty"`

	// LogLevel controls the verbosity of shfm's own diagnostic log (see
	// internal/applog) — one of "debug", "info", "warn" or "error"
	// (case-insensitive; an empty or unrecognized value, including no
	// config file at all, falls back to "warn"). "debug" is the one worth
	// knowing about: it's what turns on per-result semantic search tracing
	// (embedding/reranker scores), otherwise silent, for troubleshooting
	// relevance without a rebuild.
	LogLevel string `json:"log_level,omitempty"`

	// Notifications enables desktop notifications (via internal/notify,
	// i.e. the freedesktop.org D-Bus Notifications interface) when a task
	// (copy/move/delete/format) sent to the background finishes — whether
	// successfully, with errors, or cancelled — while the user isn't
	// watching its progress dialog. Defaults to true; set to false to opt
	// out, e.g. on a headless/SSH setup with no notification daemon, to
	// skip the per-call session-bus connection attempt.
	Notifications bool `json:"notifications"`
}

// Default returns the default configuration.
func Default() *Config {
	return &Config{DualPane: true, ShowHidden: false, LogLevel: "warn", Notifications: true}
}

func path() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	dir = filepath.Join(dir, "shfm")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load loads the configuration from disk, returning the defaults if the
// file doesn't exist yet.
func Load() *Config {
	p, err := path()
	if err != nil {
		return Default()
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Default()
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return Default()
	}
	return cfg
}

// Save writes the configuration to disk as readable JSON. The file has
// 0600 permissions: even though it never contains a plain-text password,
// it's still a "personal" file (host names, shares, network users).
func (c *Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}
