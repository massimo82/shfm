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

package opener

import (
	"bytes"
	"encoding/binary"
	"os"
	"sort"
	"strconv"
)

// Recognizing a file by its content, for when its name doesn't tell (no
// extension, or an unknown one): the Shared MIME-info database's "magic"
// rules — the binary magic file update-mime-database writes — then, as
// GNOME's GIO does when none matches, plain text or binary by the bytes
// themselves.

const (
	// maxSniff bounds how much of a file is read to recognize it, whatever
	// the rules ask for (ISO 9660 images, the farthest usual ones, are
	// recognized 32 KiB in).
	maxSniff = 256 << 10
	// textSniff is how much of a file decides plain text or binary.
	textSniff = 4096
)

// magicRule is one "[indent]>offset=value[&mask][~word-size][+range]"
// line: value found (under mask) at offset, or anywhere up to range bytes
// after it; and, when it has children, one of them matching too.
type magicRule struct {
	offset, rangeLen int
	value, mask      []byte
	children         []*magicRule
}

// magicSection is one "[priority:type]" section: the type matches when any
// of its top-level rules does.
type magicSection struct {
	priority int
	mime     string
	rules    []*magicRule
	nomagic  bool // __NOMAGIC__: no rules, and none from lower directories
	bad      bool // an unparseable header: its rules are skipped
}

// readMagic parses a magic file; nil when it's missing or not one.
func readMagic(path string) []magicSection {
	data, err := os.ReadFile(path)
	const header = "MIME-Magic\x00\n"
	if err != nil || !bytes.HasPrefix(data, []byte(header)) {
		return nil
	}
	data = data[len(header):]
	var out []magicSection
	var stack []*magicRule // stack[i]: the latest rule at indent i
	for len(data) > 0 {
		if data[0] == '[' {
			end := bytes.Index(data, []byte("]\n"))
			if end < 0 {
				break
			}
			prio, mt, ok := bytes.Cut(data[1:end], []byte(":"))
			p, err := strconv.Atoi(string(prio))
			data = data[end+2:]
			if !ok || err != nil {
				out = append(out, magicSection{bad: true})
				continue
			}
			out = append(out, magicSection{priority: p, mime: string(mt)})
			stack = stack[:0]
			continue
		}
		rule, indent, nomagic, rest, ok := parseMagicRule(data)
		data = rest
		if len(out) == 0 || out[len(out)-1].bad {
			continue
		}
		sec := &out[len(out)-1]
		switch {
		case nomagic:
			sec.rules, sec.nomagic = nil, true
		case !ok || sec.nomagic:
		case indent == 0:
			sec.rules = append(sec.rules, rule)
			stack = append(stack[:0], rule)
		case indent <= len(stack):
			parent := stack[indent-1]
			parent.children = append(parent.children, rule)
			stack = append(stack[:indent], rule)
		}
	}
	kept := out[:0]
	for _, sec := range out {
		if !sec.bad {
			kept = append(kept, sec)
		}
	}
	return kept
}

// parseMagicRule parses the rule line at the start of data, returning the
// rest of data after it. A line it can't make sense of is skipped (ok
// false), as the spec asks, for rules a later version may add.
func parseMagicRule(data []byte) (rule *magicRule, indent int, nomagic bool, rest []byte, ok bool) {
	skipLine := func(d []byte) []byte {
		if i := bytes.IndexByte(d, '\n'); i >= 0 {
			return d[i+1:]
		}
		return nil
	}
	num := func(d []byte) (int, []byte) {
		i := 0
		for i < len(d) && d[i] >= '0' && d[i] <= '9' {
			i++
		}
		n, _ := strconv.Atoi(string(d[:i]))
		return n, d[i:]
	}
	if bytes.HasPrefix(data, []byte("__NOMAGIC__")) {
		return nil, 0, true, skipLine(data), false
	}
	d := data
	indent, d = num(d)
	if len(d) == 0 || d[0] != '>' {
		return nil, 0, false, skipLine(data), false
	}
	rule = &magicRule{rangeLen: 1}
	rule.offset, d = num(d[1:])
	if len(d) < 3 || d[0] != '=' {
		return nil, 0, false, skipLine(data), false
	}
	n := int(binary.BigEndian.Uint16(d[1:3]))
	d = d[3:]
	if len(d) < n {
		return nil, 0, false, nil, false
	}
	rule.value, d = d[:n], d[n:]
	wordSize := 1
	for len(d) > 0 && d[0] != '\n' {
		switch d[0] {
		case '&':
			if len(d) < 1+n {
				return nil, 0, false, nil, false
			}
			rule.mask, d = d[1:1+n], d[1+n:]
		case '~':
			wordSize, d = num(d[1:])
		case '+':
			rule.rangeLen, d = num(d[1:])
		default:
			return nil, 0, false, skipLine(d), false
		}
	}
	if len(d) > 0 {
		d = d[1:]
	}
	if rule.rangeLen < 1 {
		rule.rangeLen = 1
	}
	// Values of a word size are written big-endian: on a little-endian
	// machine, they're compared byte-swapped.
	if (wordSize == 2 || wordSize == 4) && n%wordSize == 0 && littleEndian() {
		rule.value = swapWords(rule.value, wordSize)
		if rule.mask != nil {
			rule.mask = swapWords(rule.mask, wordSize)
		}
	}
	return rule, indent, false, d, true
}

func littleEndian() bool {
	return binary.NativeEndian.Uint16([]byte{1, 0}) == 1
}

func swapWords(b []byte, size int) []byte {
	out := make([]byte, len(b))
	for i := 0; i < len(b); i += size {
		for j := 0; j < size; j++ {
			out[i+j] = b[i+size-1-j]
		}
	}
	return out
}

// addMagic adds a directory's sections, below those of the directories
// already added: a type those have rules (or __NOMAGIC__) for keeps
// theirs. A __NOMAGIC__ section stays in the list, matching nothing, to
// do the same for the directories added next.
func (db *mimeDB) addMagic(sections []magicSection) {
	higher := map[string]bool{}
	for _, sec := range db.magic {
		higher[sec.mime] = true
	}
	for _, sec := range sections {
		if higher[sec.mime] {
			continue
		}
		db.magic = append(db.magic, sec)
		for _, r := range sec.rules {
			db.extent = max(db.extent, r.extent())
		}
	}
	sort.SliceStable(db.magic, func(i, j int) bool { return db.magic[i].priority > db.magic[j].priority })
	db.extent = min(db.extent, maxSniff)
}

func (r *magicRule) extent() int {
	e := r.offset + r.rangeLen - 1 + len(r.value)
	for _, c := range r.children {
		e = max(e, c.extent())
	}
	return e
}

func (r *magicRule) matches(data []byte) bool {
	n := len(r.value)
	for off := r.offset; off < r.offset+r.rangeLen && off+n <= len(data); off++ {
		if !r.matchesAt(data[off : off+n]) {
			continue
		}
		if len(r.children) == 0 {
			return true
		}
		for _, c := range r.children {
			if c.matches(data) {
				return true
			}
		}
	}
	return false
}

func (r *magicRule) matchesAt(b []byte) bool {
	if r.mask == nil {
		return bytes.Equal(b, r.value)
	}
	for i := range b {
		if b[i]&r.mask[i] != r.value[i]&r.mask[i] {
			return false
		}
	}
	return true
}

// sniff returns the type of content by the magic rules, the highest
// priority match first; false when none matches.
func (db *mimeDB) sniff(content []byte) (string, bool) {
	for _, sec := range db.magic {
		for _, r := range sec.rules {
			if r.matches(content) {
				return db.unalias(sec.mime), true
			}
		}
	}
	return "", false
}

// looksLikeText reports whether content reads as text: no control
// characters but whitespace, backspace and escape — GIO's test, which
// takes any 8-bit text, not only valid UTF-8.
func looksLikeText(content []byte) bool {
	for _, c := range content[:min(len(content), textSniff)] {
		if (c < 0x20 && c != '\t' && c != '\n' && c != '\r' && c != '\f' && c != '\v' && c != '\b' && c != 0x1b) || c == 0x7f {
			return false
		}
	}
	return true
}

// SniffLength is how many bytes from the start of a file DetectMimeType
// may ask for.
func SniffLength() int {
	return max(loadMimeDB().extent, textSniff)
}

// DetectMimeType returns the type of a file named name: by its name, as
// MimeType; when that tells nothing, by its content — readHead returns up
// to n bytes from the file's start (fewer at its end), only called then.
// An empty file is application/x-zerosize, other content matching no rule
// text/plain or application/octet-stream, as GNOME and KDE tell them.
func DetectMimeType(name string, readHead func(n int) ([]byte, error)) string {
	if t, ok := MimeTypeByName(name); ok {
		return t
	}
	content, err := readHead(SniffLength())
	if err != nil {
		return "application/octet-stream"
	}
	return MimeTypeOfContent(content)
}

// MimeTypeOfContent returns the type of a file starting with content (as
// much of it as SniffLength asks for), by content alone.
func MimeTypeOfContent(content []byte) string {
	if len(content) == 0 {
		return "application/x-zerosize"
	}
	if t, ok := loadMimeDB().sniff(content); ok {
		return t
	}
	if looksLikeText(content) {
		return "text/plain"
	}
	return "application/octet-stream"
}
