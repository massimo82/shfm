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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// pandoc and LibreOffice are optional system dependencies — shfm never
// bundles or installs them — but when present on $PATH they read document
// formats far more completely than this package's own minimal parsers (see
// docxText: paragraph text only, no tables/headers/footers/footnotes), and
// read several formats (.doc, .rtf, .odt) this package can't parse at all
// on its own. When found, they're preferred over the internal .docx reader
// too; when absent, extraction is exactly what it was before this file
// existed — pure Go, .txt/.md/.pdf/.docx only.
var (
	toolsOnce   sync.Once
	pandocPath  string // "" if not found
	sofficePath string // "" if not found (tried as "libreoffice", then "soffice")
)

func locateExternalTools() {
	toolsOnce.Do(func() {
		if p, err := exec.LookPath("pandoc"); err == nil {
			pandocPath = p
		}
		for _, name := range []string{"libreoffice", "soffice"} {
			if p, err := exec.LookPath(name); err == nil {
				sofficePath = p
				break
			}
		}
	})
}

// pandocFormats are the extensions pandoc's own readers handle directly —
// no LibreOffice round-trip needed first. .tex isn't otherwise unreadable
// without pandoc (extract.go's own extensions map always reads it as plain
// text, same as .txt/.md), but pandoc's LaTeX reader strips macros/
// environments down to their actual prose instead of leaving them as
// literal `\section{...}`-style noise in the extracted text, so it's
// preferred here too whenever it's available.
var pandocFormats = map[string]string{
	".docx": "docx",
	".rtf":  "rtf",
	".odt":  "odt",
	".tex":  "latex",
}

// sofficeFormats are every extension LibreOffice can open for this
// package's purposes: pandocFormats' own set (LibreOffice is the fallback
// path there when pandoc alone isn't installed) plus .doc, which nothing
// else here can read at all — parsing legacy binary Word documents
// reliably without a native dependency isn't practical.
var sofficeFormats = map[string]bool{
	".doc": true, ".docx": true, ".rtf": true, ".odt": true,
}

const externalTimeout = 30 * time.Second

// externalSupported reports whether extractExternal can handle ext, given
// whatever of pandoc/LibreOffice were actually found on this machine —
// unlike the static extensions map, this can only be answered once the
// tools have been located.
func externalSupported(ext string) bool {
	locateExternalTools()
	if pandocPath != "" {
		if _, ok := pandocFormats[ext]; ok {
			return true
		}
	}
	return sofficePath != "" && sofficeFormats[ext]
}

// extractExternal pulls markdown text out of path using pandoc and/or
// LibreOffice headless, whichever combination actually handles path's
// extension. ok reports whether an external tool handled it at all — when
// false, the caller (Text) falls back to its own internal parsers, same as
// if this file didn't exist.
func extractExternal(path string) (text string, ok bool, err error) {
	locateExternalTools()
	ext := strings.ToLower(filepath.Ext(path))

	if pandocPath != "" {
		if _, direct := pandocFormats[ext]; direct {
			text, err := runPandoc(path, "")
			return text, true, err
		}
	}

	if sofficePath == "" || !sofficeFormats[ext] {
		return "", false, nil
	}

	// pandoc can't read this format directly (chiefly legacy .doc, the
	// reason this branch exists at all) — or isn't installed. LibreOffice
	// converts it to .docx first; from there pandoc (if present) writes
	// the actual markdown, otherwise LibreOffice's own plain-text export
	// is the best available fallback.
	tmpDir, err := os.MkdirTemp("", "shfm-extract-*")
	if err != nil {
		return "", true, err
	}
	defer os.RemoveAll(tmpDir)

	targetFormat := "docx"
	if pandocPath == "" {
		targetFormat = "txt"
	}
	converted, err := runSofficeConvert(path, tmpDir, targetFormat)
	if err != nil {
		return "", true, fmt.Errorf("converting %s via LibreOffice: %w", filepath.Base(path), err)
	}
	if pandocPath == "" {
		data, err := os.ReadFile(converted)
		// LibreOffice's plain-text export leads with a UTF-8 BOM; harmless
		// for embedding, but there's no reason to keep it.
		data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
		return string(data), true, err
	}
	text, err = runPandoc(converted, "docx")
	return text, true, err
}

// runPandoc converts path to markdown via pandoc, returning its stdout.
// fromFormat overrides pandoc's own extension-based format sniffing when
// set — needed for a LibreOffice-produced intermediate .docx, since its
// path is a temp file pandoc would otherwise sniff correctly anyway, but
// being explicit here removes any doubt.
func runPandoc(path, fromFormat string) (string, error) {
	args := []string{"-t", "markdown", "--wrap=none"}
	if fromFormat != "" {
		args = append(args, "-f", fromFormat)
	}
	args = append(args, path)

	ctx, cancel := context.WithTimeout(context.Background(), externalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, pandocPath, args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pandoc: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// runSofficeConvert converts path to targetFormat (e.g. "docx", "txt") into
// outDir via LibreOffice headless, returning the resulting file's path.
//
// -env:UserInstallation gives this run its own throwaway profile directory
// rather than sharing the user's real LibreOffice profile: headless
// instances that share a profile/lock can fail to start if a GUI
// LibreOffice (or another headless run) is already using it, and shfm has
// no reason to touch the user's actual settings anyway.
func runSofficeConvert(path, outDir, targetFormat string) (string, error) {
	profileDir, err := os.MkdirTemp("", "shfm-soffice-profile-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(profileDir)

	ctx, cancel := context.WithTimeout(context.Background(), externalTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sofficePath,
		"--headless", "--norestore", "--nolockcheck",
		"-env:UserInstallation=file://"+profileDir,
		"--convert-to", targetFormat, "--outdir", outDir, path)
	// LibreOffice prints its own "convert ... using filter : ..." progress
	// line to stdout on success — discarded explicitly (not just left
	// unset) so it can never end up on the real terminal shfm's TUI holds:
	// only stderr is worth capturing, for the error message on failure.
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}

	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	out := filepath.Join(outDir, base+"."+targetFormat)
	if _, err := os.Stat(out); err != nil {
		return "", fmt.Errorf("expected output %s not found", out)
	}
	return out, nil
}
