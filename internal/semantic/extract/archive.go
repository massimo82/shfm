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
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// archiveKind reports which archive format Text should treat name as,
// judged purely by filename suffix (no content sniffing) — "" if it isn't
// one of the archive types this package recognises at all. Compound
// suffixes (".tar.gz", ".tar.bz2", ".tar.xz" and their short aliases) are
// checked before their single-extension counterparts (".gz", ".bz2",
// ".xz"), since e.g. a ".tar.bz2" ends with both.
//
// This reports a format shfm *recognises*, not necessarily one it can
// currently *read* — .xz/.lzma/.7z need an external tool found on this
// machine (see locateArchiveTools/archiveKindAvailable), same tiering as
// DOC/RTF/ODT in external.go. Supported is what callers outside this file
// should actually ask.
func archiveKind(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "targz"
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"), strings.HasSuffix(lower, ".tbz"):
		return "tarbz2"
	case strings.HasSuffix(lower, ".tar.xz"), strings.HasSuffix(lower, ".txz"):
		return "tarxz"
	case strings.HasSuffix(lower, ".zip"):
		return "zip"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	case strings.HasSuffix(lower, ".gz"):
		return "gzip"
	case strings.HasSuffix(lower, ".bz2"):
		return "bzip2"
	case strings.HasSuffix(lower, ".xz"):
		return "xz"
	case strings.HasSuffix(lower, ".lzma"):
		return "lzma"
	case strings.HasSuffix(lower, ".7z"):
		return "7z"
	}
	return ""
}

// archiveKindAvailable reports whether this machine can actually read
// archiveKind's kind: the stdlib-backed formats always can; .xz/.lzma need
// the xz command, .7z needs 7z/7zz/7za — none of which shfm bundles or
// requires (see locateArchiveTools).
func archiveKindAvailable(kind string) bool {
	switch kind {
	case "zip", "tar", "targz", "gzip", "tarbz2", "bzip2":
		return true
	case "tarxz", "xz", "lzma":
		locateArchiveTools()
		return xzPath != ""
	case "7z":
		locateArchiveTools()
		return sevenZipPath != ""
	}
	return false
}

// xz and 7z (7-Zip) are optional system dependencies, same as pandoc/
// LibreOffice in external.go: located once, lazily, and cached. xz handles
// both .xz and legacy .lzma transparently; 7-Zip ships under different
// binary names depending on distro/packaging (p7zip's "7z"/"7za", or the
// newer official Linux build's "7zz"), so all three are tried.
var (
	archiveToolsOnce sync.Once
	xzPath           string
	sevenZipPath     string
)

func locateArchiveTools() {
	archiveToolsOnce.Do(func() {
		if p, err := exec.LookPath("xz"); err == nil {
			xzPath = p
		}
		for _, name := range []string{"7z", "7zz", "7za"} {
			if p, err := exec.LookPath(name); err == nil {
				sevenZipPath = p
				break
			}
		}
	})
}

// archiveText concatenates the extracted text of every indexable document
// found inside the archive at path (kind identifies which format, from
// archiveKind), so the archive itself becomes one searchable "document".
//
// Each matched entry's bytes are written to a temp file and run back
// through Text() recursively — reusing every format this package (and
// external.go's pandoc/LibreOffice) already knows how to read, rather than
// reimplementing PDF/DOCX/etc. parsing against an in-memory reader. Nested
// archives are deliberately never recursed into: one level is enough for
// the realistic case (a folder of documents zipped up) without opening the
// door to a crafted archive-of-archives blowing up extraction time.
func archiveText(path, kind string) (string, error) {
	switch kind {
	case "zip":
		return zipText(path)
	case "tar":
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		return tarText(f)
	case "targz":
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return "", err
		}
		defer gz.Close()
		return tarText(gz)
	case "tarbz2":
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		return tarText(bzip2.NewReader(f))
	case "tarxz":
		return xzTarText(path)
	case "gzip":
		return gzipText(path)
	case "bzip2":
		return bzip2Text(path)
	case "xz", "lzma":
		return xzText(path)
	case "7z":
		return sevenZText(path)
	default:
		return "", fmt.Errorf("extract: unknown archive kind %q", kind)
	}
}

func zipText(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	tmpDir, err := os.MkdirTemp("", "shfm-archive-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	var b strings.Builder
	count, totalSize := 0, 0
	for _, f := range zr.File {
		if count >= maxArchiveEntries || totalSize >= maxArchiveTotalSize {
			break
		}
		if f.FileInfo().IsDir() || archiveKind(f.Name) != "" || !Supported(f.Name) {
			continue
		}
		if int64(f.UncompressedSize64) > maxArchiveEntrySize {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue // one unreadable entry shouldn't sink the whole archive
		}
		text, n, err := extractArchiveEntry(tmpDir, f.Name, rc, count)
		rc.Close()
		if err != nil {
			continue
		}
		writeArchiveEntry(&b, f.Name, text)
		count++
		totalSize += n
	}
	return b.String(), nil
}

func tarText(r io.Reader) (string, error) {
	tmpDir, err := os.MkdirTemp("", "shfm-archive-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	tr := tar.NewReader(r)
	var b strings.Builder
	count, totalSize := 0, 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if count >= maxArchiveEntries || totalSize >= maxArchiveTotalSize {
			break
		}
		if hdr.Typeflag != tar.TypeReg || archiveKind(hdr.Name) != "" || !Supported(hdr.Name) {
			continue
		}
		if hdr.Size > maxArchiveEntrySize {
			continue
		}
		text, n, err := extractArchiveEntry(tmpDir, hdr.Name, tr, count)
		if err != nil {
			continue
		}
		writeArchiveEntry(&b, hdr.Name, text)
		count++
		totalSize += n
	}
	return b.String(), nil
}

// gzipText handles a plain .gz — a single compressed file, not an archive
// of many. The "inner" name (needed to know which extractor to dispatch
// to) comes from gzip's own header when present, falling back to path
// itself with ".gz" trimmed off (e.g. "report.docx.gz" -> "report.docx").
func gzipText(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	name := gz.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if !Supported(name) {
		return "", fmt.Errorf("extract: %s's inner file %q isn't a supported type", filepath.Base(path), name)
	}

	tmpDir, err := os.MkdirTemp("", "shfm-archive-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	text, _, err := extractArchiveEntry(tmpDir, name, gz, 0)
	return text, err
}

// bzip2Text handles a plain .bz2, the same single-compressed-file case as
// gzipText — but bzip2's stream format carries no original filename (unlike
// gzip's optional header), so the inner name always comes from trimming
// ".bz2" off path's own basename.
func bzip2Text(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !Supported(name) {
		return "", fmt.Errorf("extract: %s's inner file %q isn't a supported type", filepath.Base(path), name)
	}

	tmpDir, err := os.MkdirTemp("", "shfm-archive-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	text, _, err := extractArchiveEntry(tmpDir, name, bzip2.NewReader(f), 0)
	return text, err
}

// xzText handles a plain .xz or legacy .lzma — same single-file case as
// gzipText/bzip2Text, decompressed by shelling out to the xz command
// (there's no xz/lzma decoder in the Go standard library).
func xzText(path string) (string, error) {
	locateArchiveTools()
	if xzPath == "" {
		return "", fmt.Errorf("extract: %s needs the xz command, which isn't installed", filepath.Base(path))
	}

	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !Supported(name) {
		return "", fmt.Errorf("extract: %s's inner file %q isn't a supported type", filepath.Base(path), name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), externalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, xzPath, "-dc", path)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("xz: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	tmpDir, err := os.MkdirTemp("", "shfm-archive-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	text, _, err := extractArchiveEntry(tmpDir, name, &out, 0)
	return text, err
}

// xzTarText handles .tar.xz/.txz: xz decompresses to stdout, streamed
// straight into tarText rather than buffered in memory first, since a tar
// archive's contents can be considerably larger than a single compressed
// document.
func xzTarText(path string) (string, error) {
	locateArchiveTools()
	if xzPath == "" {
		return "", fmt.Errorf("extract: %s needs the xz command, which isn't installed", filepath.Base(path))
	}

	ctx, cancel := context.WithTimeout(context.Background(), externalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, xzPath, "-dc", path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	text, textErr := tarText(stdout)
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("xz: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return text, textErr
}

// sevenZText handles .7z. Unlike zip/tar, there's no convenient way to
// stream individual members out of a 7z archive across every 7-Zip build
// (7z/7zz/7za) shfm might find — instead the whole archive is extracted to
// a throwaway temp directory in one shot, then walked exactly like any
// other folder shfm indexes, skipping nested archives and anything
// extract.Supported doesn't recognise.
func sevenZText(path string) (string, error) {
	locateArchiveTools()
	if sevenZipPath == "" {
		return "", fmt.Errorf("extract: %s needs a 7z command (7z/7zz/7za), which isn't installed", filepath.Base(path))
	}

	tmpDir, err := os.MkdirTemp("", "shfm-archive-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)

	ctx, cancel := context.WithTimeout(context.Background(), externalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sevenZipPath, "x", "-o"+tmpDir, "-y", "-bd", path)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("7z: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var b strings.Builder
	count, totalSize := 0, 0
	err = filepath.WalkDir(tmpDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable entries, don't abort the walk
		}
		if d.IsDir() {
			return nil
		}
		if count >= maxArchiveEntries || totalSize >= maxArchiveTotalSize {
			return filepath.SkipAll
		}
		rel, relErr := filepath.Rel(tmpDir, p)
		if relErr != nil {
			rel = d.Name()
		}
		if archiveKind(rel) != "" || !Supported(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxArchiveEntrySize {
			return nil
		}
		text, err := Text(p)
		if err != nil || strings.TrimSpace(text) == "" {
			return nil
		}
		writeArchiveEntry(&b, rel, text)
		count++
		totalSize += int(info.Size())
		return nil
	})
	if err != nil {
		return "", err
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
