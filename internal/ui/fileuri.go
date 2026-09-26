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
	"net/url"
	"strings"

	"shfm/internal/vfs"
)

// File URIs for the shared system clipboard (see sysclip.go). A local
// file is "file:///abs/path"; a file on another source is that source's
// label ("smb://host/share", "nfs://host/export", "sftp://user@host",
// "mtp://<device>") followed by its path within the source — the same
// smb:// and sftp:// URIs other file managers (e.g. Dolphin) use.
// Everything after "scheme://" is percent-encoded segment by segment.

// fileURI returns the URI of path on fs.
func fileURI(fs vfs.FileSystem, path string) string {
	if fs.Kind() == vfs.KindLocal {
		return "file://" + escapeSegments(path)
	}
	scheme, rest, ok := strings.Cut(fs.Label(), "://")
	if !ok {
		return ""
	}
	return scheme + "://" + escapeSegments(rest+path)
}

func escapeSegments(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// decodeURI turns a URI into its scheme and the decoded rest
// ("host/share/dir/file"); ok is false if it isn't "scheme://...".
func decodeURI(u string) (scheme, rest string, ok bool) {
	scheme, rest, ok = strings.Cut(strings.TrimSpace(u), "://")
	if !ok || scheme == "" {
		return "", "", false
	}
	segs := strings.Split(rest, "/")
	for i, s := range segs {
		d, err := url.PathUnescape(s)
		if err != nil {
			return "", "", false
		}
		segs[i] = d
	}
	return strings.ToLower(scheme), strings.Join(segs, "/"), true
}

// pathUnder matches a decoded URI against a source label ("smb://h/s"),
// returning the path within that source ("/" for the source's root). A
// port in the URI's authority ("sftp://u@h:22/...") is ignored if the
// label has none, since labels never carry one.
func pathUnder(label, scheme, rest string) (string, bool) {
	lScheme, lRest, ok := strings.Cut(label, "://")
	if !ok || !strings.EqualFold(lScheme, scheme) {
		return "", false
	}
	candidates := []string{rest}
	if auth, path, _ := strings.Cut(rest, "/"); strings.Contains(auth, ":") && !strings.Contains(lRest, ":") {
		host, _, _ := strings.Cut(auth, ":")
		candidates = append(candidates, host+"/"+path)
	}
	for _, r := range candidates {
		if r == lRest || r == lRest+"/" {
			return "/", true
		}
		if strings.HasPrefix(r, lRest+"/") {
			return "/" + strings.TrimPrefix(r, lRest+"/"), true
		}
	}
	return "", false
}

// parseURIList parses a text/uri-list (RFC 2483: CRLF-separated, '#'
// comments) or an x-special/gnome-copied-files payload (an action line,
// "copy" or "cut", then one URI per line).
func parseURIList(data []byte) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "://") {
			continue
		}
		out = append(out, line)
	}
	return out
}
