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

// Package archive recognises archive and compressed-file formats by name
// and reads their entries one by one, whatever the format: the common
// ground of extracting an archive into a folder (fileops.Extract) and
// indexing the documents inside one for semantic search
// (internal/semantic/extract).
//
// ZIP, TAR, gzip and bzip2 are read with the Go standard library alone;
// xz/lzma, zstd, lzip, lz4, 7z and RAR need an external tool found on this
// machine (see tools.go) — none of which shfm bundles or requires.
package archive

import "strings"

// Kind identifies an archive or compressed-file format.
type Kind string

const (
	KindZip      Kind = "zip"
	KindTar      Kind = "tar"
	KindTarGz    Kind = "targz"
	KindTarBz2   Kind = "tarbz2"
	KindTarXz    Kind = "tarxz"
	KindTarLzma  Kind = "tarlzma"
	KindTarZstd  Kind = "tarzstd"
	KindTarLzip  Kind = "tarlzip"
	KindTarLz4   Kind = "tarlz4"
	KindGzip     Kind = "gzip"
	KindBzip2    Kind = "bzip2"
	KindXz       Kind = "xz"
	KindLzma     Kind = "lzma"
	KindZstd     Kind = "zstd"
	KindLzip     Kind = "lzip"
	KindLz4      Kind = "lz4"
	KindSevenZip Kind = "7z"
	KindRar      Kind = "rar"
)

// suffixes maps every recognised filename suffix to its Kind. Order
// matters: compound suffixes (".tar.gz") come before the single-extension
// ones they end with (".gz").
var suffixes = []struct {
	suffix string
	kind   Kind
}{
	{".tar.gz", KindTarGz}, {".tgz", KindTarGz},
	{".tar.bz2", KindTarBz2}, {".tbz2", KindTarBz2}, {".tbz", KindTarBz2},
	{".tar.xz", KindTarXz}, {".txz", KindTarXz},
	{".tar.lzma", KindTarLzma}, {".tlz", KindTarLzma},
	{".tar.zst", KindTarZstd}, {".tzst", KindTarZstd},
	{".tar.lz", KindTarLzip},
	{".tar.lz4", KindTarLz4},
	{".zip", KindZip},
	{".tar", KindTar},
	{".gz", KindGzip},
	{".bz2", KindBzip2},
	{".xz", KindXz},
	{".lzma", KindLzma},
	{".zst", KindZstd},
	{".lz", KindLzip},
	{".lz4", KindLz4},
	{".7z", KindSevenZip},
	{".rar", KindRar},
}

// Detect reports which format name (a filename or path) is, judged purely
// by its suffix, case-insensitively — no content sniffing — and name's
// base with that suffix removed ("photos.tar.gz" → "photos"). kind is ""
// when name isn't an archive at all.
//
// This is a format shfm *recognises*, not necessarily one it can read on
// this machine: ask Available for that.
func Detect(name string) (kind Kind, base string) {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	lower := strings.ToLower(name)
	for _, s := range suffixes {
		// An entry named just ".zip" is a dotfile, not an archive.
		if strings.HasSuffix(lower, s.suffix) && len(name) > len(s.suffix) {
			return s.kind, name[:len(name)-len(s.suffix)]
		}
	}
	return "", name
}

// IsArchive reports whether name is a recognised archive or compressed
// file (see Detect).
func IsArchive(name string) bool {
	kind, _ := Detect(name)
	return kind != ""
}

// Single reports whether k is one compressed file rather than an
// archive of many entries.
func (k Kind) Single() bool {
	switch k {
	case KindGzip, KindBzip2, KindXz, KindLzma, KindZstd, KindLzip, KindLz4:
		return true
	}
	return false
}

// tarFilter returns, for a compressed-tar kind, the single-file kind of
// its compression layer ("" for a plain tar or a non-tar kind).
func (k Kind) tarFilter() Kind {
	switch k {
	case KindTarGz:
		return KindGzip
	case KindTarBz2:
		return KindBzip2
	case KindTarXz:
		return KindXz
	case KindTarLzma:
		return KindLzma
	case KindTarZstd:
		return KindZstd
	case KindTarLzip:
		return KindLzip
	case KindTarLz4:
		return KindLz4
	}
	return ""
}

func (k Kind) isTar() bool { return k == KindTar || k.tarFilter() != "" }
