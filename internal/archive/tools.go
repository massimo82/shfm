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

package archive

import (
	"fmt"
	"strings"
	"sync"

	"shfm/internal/toolpath"
)

// The external tools are optional system dependencies: located once,
// lazily, and cached. Each one may ship under several binary names
// depending on distro/packaging (p7zip's "7z"/"7za" or the official
// build's "7zz"; lzip or its parallel plzip), so all of them are tried.
//
// bsdtar (libarchive) is the universal fallback: it reads 7z, RAR and
// every compressed tar, and is turned into a plain tar stream (see
// bsdtarArgs) so its entries go through the same checks as any other.
var toolNames = map[string][]string{
	"xz":     {"xz"},
	"bzip2":  {"bzip2"},
	"zstd":   {"zstd"},
	"lzip":   {"lzip", "plzip"},
	"lz4":    {"lz4"},
	"7z":     {"7z", "7zz", "7za"},
	"unrar":  {"unrar"},
	"bsdtar": {"bsdtar"},
}

var (
	toolsMu   sync.Mutex
	toolPaths map[string]string
	// lookPath is toolpath.Find (PATH, then the usual folders), replaced
	// by tests to simulate a machine without some tool.
	lookPath = toolpath.Find
)

// toolPath returns the path of the external tool (a toolNames key), ""
// when none of its binaries is installed.
func toolPath(tool string) string {
	toolsMu.Lock()
	defer toolsMu.Unlock()
	if toolPaths == nil {
		toolPaths = map[string]string{}
		for t, names := range toolNames {
			for _, n := range names {
				if p, err := lookPath(n); err == nil {
					toolPaths[t] = p
					break
				}
			}
		}
	}
	return toolPaths[tool]
}

// resetTools forgets the located tools, for tests that replace lookPath.
func resetTools() {
	toolsMu.Lock()
	toolPaths = nil
	toolsMu.Unlock()
}

// filterTool returns the external decompressor for a single-file kind
// that the standard library can't read ("" for gzip/bzip2, or when kind
// isn't a single-file kind).
func filterTool(kind Kind) string {
	switch kind {
	case KindXz, KindLzma: // xz reads legacy .lzma too
		return "xz"
	case KindZstd:
		return "zstd"
	case KindLzip:
		return "lzip"
	case KindLz4:
		return "lz4"
	}
	return ""
}

// Available reports whether this machine can read kind: the formats the
// standard library handles always can; the others need their tool (see
// toolNames) or, for everything but a single compressed file, bsdtar.
func Available(kind Kind) bool {
	return missingTool(kind) == nil
}

// Supported reports whether name is an archive this machine can read.
func Supported(name string) bool {
	kind, _ := Detect(name)
	return kind != "" && Available(kind)
}

// ReadNeeds says what to install to read kind ("xz or bsdtar"), "" when
// this machine already can.
func ReadNeeds(kind Kind) string {
	return orList(readNeeds(kind))
}

// readNeeds returns the tools any one of which reading kind needs, nil
// when this machine already can (or kind is unknown).
func readNeeds(kind Kind) []string {
	switch kind {
	case KindZip, KindTar, KindTarGz, KindTarBz2, KindGzip, KindBzip2:
		return nil
	case KindSevenZip:
		if toolPath("bsdtar") != "" || toolPath("7z") != "" {
			return nil
		}
		return []string{"bsdtar", "7-Zip"}
	case KindRar:
		if toolPath("bsdtar") != "" || toolPath("unrar") != "" || toolPath("7z") != "" {
			return nil
		}
		return []string{"bsdtar", "unrar", "7-Zip"}
	}
	if kind.Single() {
		if tool := filterTool(kind); toolPath(tool) == "" {
			return []string{tool}
		}
		return nil
	}
	if f := kind.tarFilter(); f != "" {
		if tool := filterTool(f); toolPath(tool) == "" && toolPath("bsdtar") == "" {
			return []string{tool, "bsdtar"}
		}
	}
	return nil
}

// missingTool explains what needs installing to read kind, nil when
// nothing does.
func missingTool(kind Kind) error {
	if kind.Ext() == "" {
		return fmt.Errorf("unknown archive format %q", kind)
	}
	return missingError("reading", kind, readNeeds(kind))
}

// missingError explains that doing verb to kind needs one of tools, nil
// when there are none to install.
func missingError(verb string, kind Kind, tools []string) error {
	switch len(tools) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("%s %s needs %s, which isn't installed", verb, kind.Ext(), tools[0])
	case 2:
		return fmt.Errorf("%s %s needs %s, and neither is installed", verb, kind.Ext(), orList(tools))
	}
	return fmt.Errorf("%s %s needs %s, and none is installed", verb, kind.Ext(), orList(tools))
}

// orList joins names as "a", "a or b", "a, b or c".
func orList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
