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

// Package applog is shfm's own diagnostic log, distinct from both the
// TUI's stdout/stderr (owned by bubbletea's alt-screen while the program
// runs — writing routine diagnostics there would corrupt the display) and
// internal/semantic's llama.log (which captures llama.cpp's own native
// C-level logging via an fd-level redirect, not shfm's Go code). Anything
// shfm itself wants to record for later troubleshooting — without
// interrupting the user or needing a debug rebuild — goes here instead:
// $XDG_CACHE_HOME/shfm/logs/shfm.log, as structured (slog) text.
package applog

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	mu     sync.Mutex
	logger *slog.Logger
	file   *os.File
)

// ParseLevel maps a config/user-facing level name ("debug", "info", "warn"
// or "error", case-insensitive) to its slog.Level, defaulting to
// slog.LevelWarn for an empty or unrecognized value — the same fallback
// config.Config.LogLevel documents, so no config file at all, an old one
// predating this field, or a typo all degrade to a sane (quiet-by-default)
// level rather than failing startup.
func ParseLevel(name string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	default: // "warn"/"warning", "", or anything unrecognized
		return slog.LevelWarn
	}
}

// Init opens (creating if needed) $XDG_CACHE_HOME/shfm/logs/shfm.log for
// appending and routes subsequent package-level calls to it, as slog's
// standard key=value text format, filtered to level and above. Safe to
// call more than once — later calls are no-ops once already initialized.
func Init(level slog.Level) error {
	mu.Lock()
	defer mu.Unlock()
	if logger != nil {
		return nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(cacheDir, "shfm", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "shfm.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	file = f
	logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level}))
	return nil
}

// Close flushes and closes the log file, if one is open. Safe to call even
// if Init was never called or failed.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		_ = file.Close()
		file, logger = nil, nil
	}
}

// current returns the active logger, or a discarding one if Init hasn't
// succeeded — callers never need to check availability themselves, so a
// logging failure never becomes a reason to skip work or surface an error
// the user didn't ask for.
func current() *slog.Logger {
	mu.Lock()
	defer mu.Unlock()
	if logger != nil {
		return logger
	}
	return slog.New(slog.NewTextHandler(discard{}, nil))
}

// discard is an io.Writer that drops everything written to it.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Debug, Info, Warn and Error log one structured line at the given level,
// with attrs as alternating key/value pairs (slog's own convention, e.g.
// Info("search done", "query", q, "results", n)).
func Debug(msg string, attrs ...any) { current().Debug(msg, attrs...) }
func Info(msg string, attrs ...any)  { current().Info(msg, attrs...) }
func Warn(msg string, attrs ...any)  { current().Warn(msg, attrs...) }
func Error(msg string, attrs ...any) { current().Error(msg, attrs...) }
