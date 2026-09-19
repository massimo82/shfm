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

// Package extract pulls plain text out of the file types semantic search
// (see internal/semantic) can index: TXT/Markdown, PDF and DOCX always;
// DOC, RTF and ODT too when pandoc and/or LibreOffice are found installed
// on the system (see external.go) — shfm never bundles or requires either,
// so this is a graceful upgrade, not a hard dependency. ZIP/TAR/TAR.GZ/GZ
// archives are also indexable (see archive.go): every supported document
// found inside becomes part of the archive's own extracted text, since
// semantic search has no notion of a path *inside* an archive — a match
// there surfaces the archive file itself. It has no dependency on the LLM/
// vector-store machinery, so it can be built and tested on its own even
// where the heavier cgo pieces (github.com/tcpipuk/llama-go) can't compile
// — see internal/semantic's doc comment.
package extract

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"
)

// extensions are the file extensions this package knows how to pull text
// out of. .tex (LaTeX source) is plain text like .txt/.md as far as this
// fallback path is concerned — its markup stays as literal `\section{...}`
// commands in the extracted text; see external.go's pandocFormats for the
// cleaner alternative when pandoc is available.
var extensions = map[string]bool{
	".txt": true, ".md": true, ".tex": true, ".pdf": true, ".docx": true,
}

// Supported reports whether name (a filename or path — needs more than
// just its filepath.Ext, since recognising a ".tar.gz" needs the two-part
// suffix, not just ".gz") is a type Text can extract from: always for TXT/
// Markdown/PDF/DOCX/ZIP/TAR/TAR.GZ/GZ/BZ2/TAR.BZ2; DOC/RTF/ODT because
// pandoc/LibreOffice were found (see external.go), or XZ/LZMA/7Z because xz
// and/or a 7-Zip build were found (see archive.go) — on this machine.
func Supported(name string) bool {
	if kind := archiveKind(name); kind != "" {
		return archiveKindAvailable(kind)
	}
	ext := strings.ToLower(filepath.Ext(name))
	return extensions[ext] || externalSupported(ext)
}

// Text returns the plain-text (or, when an external tool or an archive
// handled it, markdown or a multi-document concatenation respectively)
// content of path, based on its extension.
//
// DOC, RTF and ODT only work when pandoc and/or LibreOffice are installed
// (see external.go and Supported) — shfm has no native parser for them.
// DOCX prefers those same external tools when available too, since they
// read tables, headers/footers and footnotes that docxText below doesn't
// even attempt; docxText exists purely as the no-external-tools fallback,
// same as this package's behaviour before external.go existed.
func Text(path string) (string, error) {
	if kind := archiveKind(path); kind != "" {
		return archiveText(path, kind)
	}
	if text, ok, err := extractExternal(path); ok {
		return text, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt", ".md", ".tex":
		return plainText(path)
	case ".pdf":
		return pdfText(path)
	case ".docx":
		return docxText(path)
	default:
		return "", fmt.Errorf("extract: unsupported file type %q", filepath.Ext(path))
	}
}

// plainText reads path verbatim — TXT, Markdown and (without pandoc; see
// external.go's pandocFormats) LaTeX are all already plain text as far as
// extraction is concerned, markup and all.
func plainText(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func pdfText(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	var b strings.Builder
	totalPage := r.NumPage()
	for i := 1; i <= totalPage; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			continue // an unreadable page shouldn't sink the whole document
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// docxDocument mirrors just enough of word/document.xml's structure to pull
// out the paragraph text runs, ignoring formatting, tables layout, headers/
// footers and everything else DOCX also carries.
type docxDocument struct {
	XMLName xml.Name `xml:"document"`
	Body    struct {
		Paragraphs []struct {
			Runs []struct {
				Text []struct {
					Value string `xml:",chardata"`
				} `xml:"t"`
			} `xml:"r"`
		} `xml:"p"`
	} `xml:"body"`
}

func docxText(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()

	var docFile *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docFile = f
			break
		}
	}
	if docFile == nil {
		return "", fmt.Errorf("extract: %s doesn't look like a .docx (no word/document.xml)", path)
	}
	rc, err := docFile.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	var doc docxDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, p := range doc.Body.Paragraphs {
		for _, r := range p.Runs {
			for _, t := range r.Text {
				b.WriteString(t.Value)
			}
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}
