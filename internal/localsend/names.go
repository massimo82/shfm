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

package localsend

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxNameBytes is the longest file name most file systems take.
	maxNameBytes = 255
	// maxDepth bounds the folders of a received path: a deeper one keeps
	// only its last ones.
	maxDepth = 32
)

// safePath turns the name of a received file — a path inside the folder
// sent whole, for a file in one — into "/"-separated segments safe to
// create inside the destination: "." and ".." and empty segments dropped
// (no escaping the destination), control characters (a name shown in a
// terminal) replaced, every segment at most 255 bytes. Never empty.
func safePath(name string) []string {
	var segs []string
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		seg = safeSegment(seg)
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	if len(segs) == 0 {
		return []string{"untitled"}
	}
	if len(segs) > maxDepth {
		segs = segs[len(segs)-maxDepth:]
	}
	return segs
}

func safeSegment(seg string) string {
	seg = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, seg)
	if strings.TrimSpace(seg) == "" || seg == "." || seg == ".." {
		return ""
	}
	return truncateName(seg, maxNameBytes)
}

// truncateName cuts name to at most max bytes, keeping its extension
// and whole characters.
func truncateName(name string, max int) string {
	if len(name) <= max {
		return name
	}
	ext := ""
	if i := strings.LastIndexByte(name, '.'); i > 0 && len(name)-i <= 16 {
		ext = name[i:]
	}
	base := name[:max-len(ext)]
	for !utf8.ValidString(base) {
		base = base[:len(base)-1]
	}
	return base + ext
}

// numbered is name with " (n)" before its extension: "a (2).txt".
func numbered(name string, n int) string {
	base, ext := name, ""
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		base, ext = name[:i], name[i:]
	}
	suffix := " (" + strconv.Itoa(n) + ")"
	if len(base)+len(suffix)+len(ext) > maxNameBytes {
		base = truncateName(base, maxNameBytes-len(suffix)-len(ext))
	}
	return base + suffix + ext
}
