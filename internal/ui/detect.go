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

package ui

import (
	"errors"
	"io"
	"os"

	"shfm/internal/opener"
	"shfm/internal/vfs"
)

// A file's type, when its name doesn't tell it (no extension, or an
// unknown one), is recognized by its first bytes (see
// opener.DetectMimeType): read right away on the local filesystem, in the
// background on the other sources, so a slow network or device never
// stalls the UI.

// detectedType is a type recognized in the background, and what it was
// recognized for: opening the file (realPath or remote), or the
// associations dialog (forAssoc).
type detectedType struct {
	mime     string
	name     string
	realPath string
	remote   *remoteOpenTarget
	forAssoc bool
}

// readHead returns a function reading up to n bytes from the start of the
// file at path — only that range, on sources able to read one (a stream
// would transfer the rest of the file, from an MTP device say).
func readHead(fs vfs.FileSystem, path string) func(n int) ([]byte, error) {
	return func(n int) ([]byte, error) {
		buf := make([]byte, n)
		if ra, ok := fs.(vfs.RandomAccessOpener); ok {
			f, err := ra.OpenRandom(path, os.O_RDONLY, 0)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			got, err := f.ReadAt(buf, 0)
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			return buf[:got], nil
		}
		r, err := fs.Open(path)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		got, err := io.ReadFull(r, buf)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, err
		}
		return buf[:got], nil
	}
}

// isLocalFS reports whether fs is the local filesystem (a real path an
// external application can open directly).
func isLocalFS(fs vfs.FileSystem) bool {
	_, ok := fs.(vfs.LocalPath)
	return ok
}

// detectInBackground recognizes the type of the file at path on fs by its
// content, sending the result, with d's context, to the open channel.
func (m *Model) detectInBackground(fs vfs.FileSystem, path string, d detectedType) {
	ch := m.openCh
	go func() {
		d.mime = opener.DetectMimeType(d.name, readHead(fs, path))
		ch <- openResultMsg{detected: &d}
	}()
}

// handleDetected carries on with what the type was recognized for.
func (m *Model) handleDetected(d detectedType) {
	if d.forAssoc {
		// Only if the dialog is still as it was opened.
		if m.dialog.Kind == DialogAssociations && m.dialog.Inputs[0].Value() == "" &&
			!m.dialog.AssocMine && m.dialog.ItemIdx == 0 {
			m.showAssociations("", false, d.mime)
		}
		return
	}
	// A dialog opened meanwhile isn't replaced by the application chooser.
	if _, ok := opener.DefaultApp(d.mime); !ok && m.dialog.Kind != DialogNone {
		m.setStatus("No application is set for %s (%s): open it again to choose one", d.name, d.mime)
		return
	}
	m.openWithType(d.name, d.mime, d.realPath, d.remote)
}
