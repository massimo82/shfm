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

//go:build cloud

package cloud

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDrive is an in-memory Google Drive serving the part of the REST API
// v3 the drive backend uses.
type fakeDrive struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	files    map[string]*fakeDriveFile
	drives   map[string]string // shared drive ID -> name (its root folder has the same ID)
	sessions map[string]*fakeDriveSession
	next     int
	clock    time.Time

	throttle int // answer this many requests with 403 rateLimitExceeded first
	requests int
}

type fakeDriveFile struct {
	id, name, mime string
	parents        []string
	content        []byte
	mod            time.Time
	trashed        bool
	target         string // shortcuts
	driveID        string // the shared drive holding it
	shared         bool   // shared with the account (by someone else)
}

type fakeDriveSession struct {
	fileID  string // updating it, or "" to create
	name    string
	parents []string
	data    []byte
}

func newFakeDrive(t *testing.T) *fakeDrive {
	f := &fakeDrive{
		t:        t,
		files:    map[string]*fakeDriveFile{"root": {id: "root", name: "My Drive", mime: driveFolderMime}},
		drives:   map[string]string{},
		sessions: map[string]*fakeDriveSession{},
		clock:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	oldAPI, oldUpload := driveAPI, driveUpload
	driveAPI, driveUpload = f.srv.URL+"/drive/v3", f.srv.URL+"/upload/drive/v3"
	t.Cleanup(func() { driveAPI, driveUpload = oldAPI, oldUpload })
	return f
}

// add puts a file straight into the fake (as if made elsewhere).
func (f *fakeDrive) add(parent, name, mime string, content []byte) *fakeDriveFile {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addLocked(parent, name, mime, content)
}

func (f *fakeDrive) addLocked(parent, name, mime string, content []byte) *fakeDriveFile {
	f.next++
	f.clock = f.clock.Add(time.Minute)
	if mime == "" {
		mime = "application/octet-stream"
	}
	file := &fakeDriveFile{id: fmt.Sprintf("id%d", f.next), name: name, mime: mime, parents: []string{parent}, content: content, mod: f.clock}
	if p, ok := f.files[parent]; ok {
		file.driveID = p.driveID
	}
	f.files[file.id] = file
	return file
}

// addSharedDrive makes a shared drive the account is a member of.
func (f *fakeDrive) addSharedDrive(id, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drives[id] = name
	f.files[id] = &fakeDriveFile{id: id, name: name, mime: driveFolderMime, driveID: id}
}

// addShared makes a file someone else shared with the account: in their
// own Drive, so in no folder the account can see.
func (f *fakeDrive) addShared(name, mime string, content []byte) *fakeDriveFile {
	file := f.add("someone-elses-folder", name, mime, content)
	file.shared = true
	return file
}

func (f *fakeDrive) json(file *fakeDriveFile) map[string]any {
	m := map[string]any{"id": file.id, "name": file.name, "mimeType": file.mime, "modifiedTime": file.mod.Format(time.RFC3339Nano)}
	if !strings.HasPrefix(file.mime, driveGoogleType) {
		m["size"] = strconv.Itoa(len(file.content))
	}
	if file.driveID != "" {
		m["driveId"] = file.driveID
	}
	if file.mime == driveShortcutMime {
		m["shortcutDetails"] = map[string]any{"targetId": file.target, "targetMimeType": f.files[file.target].mime}
	}
	return m
}

func driveFail(w http.ResponseWriter, status int, reason, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": msg, "errors": []any{map[string]any{"reason": reason}}}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

var (
	reDriveByName       = regexp.MustCompile(`^name = '((?:[^'\\]|\\.)*)' and '((?:[^'\\]|\\.)*)' in parents and trashed = false$`)
	reDriveIn           = regexp.MustCompile(`^'((?:[^'\\]|\\.)*)' in parents and trashed = false$`)
	reDriveShared       = regexp.MustCompile(`^sharedWithMe = true and trashed = false$`)
	reDriveSharedByName = regexp.MustCompile(`^name = '((?:[^'\\]|\\.)*)' and sharedWithMe = true and trashed = false$`)
)

func driveUnquote(s string) string {
	return strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(s)
}

func (f *fakeDrive) serve(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if f.throttle > 0 {
		f.throttle--
		driveFail(w, 403, "userRateLimitExceeded", "slow down")
		return
	}
	p := r.URL.Path
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && p == "/drive/v3/files":
		f.list(w, q)
	case r.Method == http.MethodGet && p == "/drive/v3/drives":
		drives := []any{}
		for id, name := range f.drives {
			drives = append(drives, map[string]any{"id": id, "name": name})
		}
		writeJSON(w, 200, map[string]any{"drives": drives})
	case r.Method == http.MethodGet && p == "/drive/v3/about":
		writeJSON(w, 200, map[string]any{
			"user":         map[string]any{"emailAddress": "user@example.com"},
			"storageQuota": map[string]any{"limit": "1000000000", "usage": "123"},
		})
	case r.Method == http.MethodPost && p == "/drive/v3/files":
		var meta struct {
			Name     string   `json:"name"`
			MimeType string   `json:"mimeType"`
			Parents  []string `json:"parents"`
		}
		json.NewDecoder(r.Body).Decode(&meta)
		writeJSON(w, 200, f.json(f.addLocked(meta.Parents[0], meta.Name, meta.MimeType, nil)))
	case strings.HasPrefix(p, "/drive/v3/files/"):
		rest := strings.TrimPrefix(p, "/drive/v3/files/")
		id, export, _ := strings.Cut(rest, "/")
		file, ok := f.files[id]
		if !ok || file.trashed {
			driveFail(w, 404, "notFound", "File not found: "+id)
			return
		}
		switch {
		case r.Method == http.MethodPatch:
			f.patch(w, r, file)
		case export == "export":
			if !strings.HasPrefix(file.mime, driveGoogleType) {
				driveFail(w, 403, "fileNotExportable", "not a Google document")
				return
			}
			fmt.Fprintf(w, "%s as %s", file.name, q.Get("mimeType"))
		case q.Get("alt") == "media":
			if strings.HasPrefix(file.mime, driveGoogleType) {
				driveFail(w, 403, "fileNotDownloadable", "use export")
				return
			}
			serveRange(w, r, file.content)
		default:
			writeJSON(w, 200, f.json(file))
		}
	case r.Method == http.MethodPost && p == "/upload/drive/v3/files":
		var meta struct {
			Name    string   `json:"name"`
			Parents []string `json:"parents"`
		}
		json.NewDecoder(r.Body).Decode(&meta)
		f.newSession(w, &fakeDriveSession{name: meta.Name, parents: meta.Parents})
	case r.Method == http.MethodPatch && strings.HasPrefix(p, "/upload/drive/v3/files/"):
		id := strings.TrimPrefix(p, "/upload/drive/v3/files/")
		if _, ok := f.files[id]; !ok {
			driveFail(w, 404, "notFound", "File not found")
			return
		}
		f.newSession(w, &fakeDriveSession{fileID: id})
	case r.Method == http.MethodPut && strings.HasPrefix(p, "/session/"):
		f.chunk(w, r, strings.TrimPrefix(p, "/session/"))
	default:
		driveFail(w, 400, "badRequest", "unexpected "+r.Method+" "+p)
	}
}

func (f *fakeDrive) list(w http.ResponseWriter, params url.Values) {
	q := params.Get("q")
	var name, parent string
	byName, shared := false, false
	switch {
	case reDriveByName.MatchString(q):
		m := reDriveByName.FindStringSubmatch(q)
		name, parent, byName = driveUnquote(m[1]), driveUnquote(m[2]), true
	case reDriveIn.MatchString(q):
		parent = driveUnquote(reDriveIn.FindStringSubmatch(q)[1])
	case reDriveShared.MatchString(q):
		shared = true
	case reDriveSharedByName.MatchString(q):
		name, byName, shared = driveUnquote(reDriveSharedByName.FindStringSubmatch(q)[1]), true, true
	default:
		driveFail(w, 400, "invalidQuery", "unexpected query: "+q)
		return
	}
	// As Drive does: a shared drive's files are only found asking that
	// drive (corpora=drive, driveId), the others only without.
	corpus := ""
	if params.Get("corpora") == "drive" {
		corpus = params.Get("driveId")
	}
	if !shared {
		if p, ok := f.files[parent]; !ok || p.trashed {
			driveFail(w, 404, "notFound", "File not found: "+parent)
			return
		}
	}
	var out []*fakeDriveFile
	for _, file := range f.files {
		if file.trashed || (byName && file.name != name) || file.driveID != corpus ||
			(shared && !file.shared) || (!shared && !slices.Contains(file.parents, parent)) {
			continue
		}
		out = append(out, file)
	}
	sort.Slice(out, func(i, j int) bool {
		fi, fj := out[i].mime == driveFolderMime, out[j].mime == driveFolderMime
		if fi != fj {
			return fi
		}
		return out[i].mod.After(out[j].mod)
	})
	files := []any{}
	for _, file := range out {
		files = append(files, f.json(file))
	}
	writeJSON(w, 200, map[string]any{"files": files})
}

func (f *fakeDrive) patch(w http.ResponseWriter, r *http.Request, file *fakeDriveFile) {
	var body struct {
		Name         *string `json:"name"`
		Trashed      *bool   `json:"trashed"`
		ModifiedTime *string `json:"modifiedTime"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	q := r.URL.Query()
	if add, remove := q.Get("addParents"), q.Get("removeParents"); add != "" {
		file.parents = slices.DeleteFunc(file.parents, func(p string) bool { return p == remove })
		file.parents = append(file.parents, add)
		if p, ok := f.files[add]; ok {
			file.driveID = p.driveID // moved into (or out of) a shared drive
		}
	}
	if body.Name != nil {
		file.name = *body.Name
	}
	if body.Trashed != nil {
		f.trash(file)
	}
	if body.ModifiedTime != nil {
		t, err := time.Parse(time.RFC3339Nano, *body.ModifiedTime)
		if err != nil {
			driveFail(w, 400, "invalid", "bad time")
			return
		}
		file.mod = t
	}
	writeJSON(w, 200, f.json(file))
}

// trash trashes file and, as Drive does, everything inside it.
func (f *fakeDrive) trash(file *fakeDriveFile) {
	file.trashed = true
	for _, c := range f.files {
		if slices.Contains(c.parents, file.id) && !c.trashed {
			f.trash(c)
		}
	}
}

func (f *fakeDrive) newSession(w http.ResponseWriter, s *fakeDriveSession) {
	f.next++
	id := fmt.Sprintf("s%d", f.next)
	f.sessions[id] = s
	w.Header().Set("Location", f.srv.URL+"/session/"+id)
	w.WriteHeader(200)
}

var reContentRange = regexp.MustCompile(`^bytes (?:(\d+)-(\d+)|\*)/(\d+|\*)$`)

func (f *fakeDrive) chunk(w http.ResponseWriter, r *http.Request, id string) {
	s, ok := f.sessions[id]
	if !ok {
		driveFail(w, 404, "notFound", "no such session")
		return
	}
	m := reContentRange.FindStringSubmatch(r.Header.Get("Content-Range"))
	if m == nil {
		driveFail(w, 400, "badRange", "bad Content-Range "+r.Header.Get("Content-Range"))
		return
	}
	body, _ := io.ReadAll(r.Body)
	if m[1] != "" {
		start, _ := strconv.Atoi(m[1])
		end, _ := strconv.Atoi(m[2])
		if start != len(s.data) || end-start+1 != len(body) {
			driveFail(w, 400, "badRange", fmt.Sprintf("range %d-%d with %d bytes, have %d", start, end, len(body), len(s.data)))
			return
		}
		s.data = append(s.data, body...)
	}
	if m[3] == "*" || strconv.Itoa(len(s.data)) != m[3] {
		if len(s.data) > 0 {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(s.data)-1))
		}
		w.WriteHeader(http.StatusPermanentRedirect)
		return
	}
	delete(f.sessions, id)
	var file *fakeDriveFile
	if s.fileID != "" {
		file = f.files[s.fileID]
		file.content = s.data
		f.clock = f.clock.Add(time.Minute)
		file.mod = f.clock
	} else {
		file = f.addLocked(s.parents[0], s.name, "", s.data)
	}
	writeJSON(w, 200, f.json(file))
}

// serveRange serves content honoring a "bytes=N-" Range.
func serveRange(w http.ResponseWriter, r *http.Request, content []byte) {
	rng := r.Header.Get("Range")
	if rng == "" {
		w.Write(content)
		return
	}
	var start int
	if _, err := fmt.Sscanf(rng, "bytes=%d-", &start); err != nil {
		http.Error(w, "bad range", 400)
		return
	}
	if start >= len(content) {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(content)-1, len(content)))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(content[start:])
}
