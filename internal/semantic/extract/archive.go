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

//go:build semantic

package extract

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"shfm/internal/archive"
)

const (
	// maxArchiveEntries caps how many inner documents get extracted from a
	// single archive — the same spirit as maxIndexedFiles in
	// engine_enabled.go, applied one level down.
	maxArchiveEntries = 200
	// maxArchiveEntrySize caps any single inner document's decompressed
	// size, and maxArchiveTotalSize the sum across one archive — a pair of
	// cheap guards against a crafted archive that decompresses far beyond
	// its on-disk size ("zip bomb") tying up extraction indefinitely.
	maxArchiveEntrySize = 50 << 20  // 50MB
	maxArchiveTotalSize = 200 << 20 // 200MB
)

// archiveText concatenates the extracted text of every indexable document
// found inside the archive at path (any format internal/archive reads), so
// the archive itself becomes one searchable "document".
//
// Each matched entry's bytes are written to a temp file and run back
// through Text() recursively — reusing every format this package (and
// external.go's pandoc/LibreOffice) already knows how to read, rather than
// reimplementing PDF/DOCX/etc. parsing against an in-memory reader. Nested
// archives are deliberately never recursed into: one level is enough for
// the realistic case (a folder of documents zipped up) without opening the
// door to a crafted archive-of-archives blowing up extraction time.
func archiveText(path string) (string, error) {
	tmpDir, err := os.MkdirTemp("", "shfm-archive-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), externalTimeout)
	defer cancel()

	var b strings.Builder
	count, totalSize := 0, 0
	src := archive.Source{Name: filepath.Base(path), LocalPath: path}
	err = archive.Walk(ctx, src, func(e archive.Entry, r io.Reader) error {
		if count >= maxArchiveEntries || totalSize >= maxArchiveTotalSize {
			return archive.ErrStop
		}
		if r == nil || archive.IsArchive(e.Name) || !Supported(e.Name) || e.Size > maxArchiveEntrySize {
			return nil
		}
		text, n, err := extractArchiveEntry(tmpDir, e.Name, r, count)
		totalSize += n
		if err != nil {
			return nil // one unreadable entry shouldn't sink the whole archive
		}
		writeArchiveEntry(&b, e.Name, text)
		count++
		return nil
	})
	if err != nil {
		return "", err
	}
	if kind, _ := archive.Detect(path); count == 0 && kind.Single() {
		// A lone compressed file is only worth indexing as its content.
		return "", fmt.Errorf("extract: no usable text inside %s", filepath.Base(path))
	}
	return b.String(), nil
}

// extractArchiveEntry writes r (one archive entry's content, capped at
// maxArchiveEntrySize) to a temp file under tmpDir — named by index to
// avoid collisions between entries that share a basename, but keeping
// name's extension so Text can dispatch on it — then runs it back through
// Text(). Returns the extracted text and how many bytes were actually
// written (for the caller's running total against maxArchiveTotalSize).
func extractArchiveEntry(tmpDir, name string, r io.Reader, index int) (text string, written int, err error) {
	tmpPath := filepath.Join(tmpDir, fmt.Sprintf("%d%s", index, filepath.Ext(name)))
	out, err := os.Create(tmpPath)
	if err != nil {
		return "", 0, err
	}
	n, err := io.Copy(out, io.LimitReader(r, maxArchiveEntrySize))
	closeErr := out.Close()
	written = int(n)
	if err != nil {
		return "", written, err
	}
	if closeErr != nil {
		return "", written, closeErr
	}
	text, err = Text(tmpPath)
	os.Remove(tmpPath)
	if err != nil {
		return "", written, err
	}
	if strings.TrimSpace(text) == "" {
		return "", written, fmt.Errorf("extract: no usable text from %s", name)
	}
	return text, written, nil
}

func writeArchiveEntry(b *strings.Builder, name, text string) {
	b.WriteString("--- ")
	b.WriteString(name)
	b.WriteString(" ---\n")
	b.WriteString(text)
	b.WriteString("\n")
}
