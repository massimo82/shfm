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
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDropbox is an in-memory Dropbox serving the part of the API v2 the
// dropbox backend uses, through the SDK. Like Dropbox, it ignores case in
// paths.
type fakeDropbox struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	items    map[string]*fakeDbxItem // by lowercased path
	sessions map[string][]byte
	next     int
	clock    time.Time
	throttle int
	// tokenLacks and appLacks: a permission the token, or the app itself,
	// doesn't have, failing the files/ routes as Dropbox does.
	tokenLacks, appLacks string
}

type fakeDbxItem struct {
	path, id string
	dir      bool
	content  []byte
	mod      time.Time
}

const dbxTime = "2006-01-02T15:04:05Z"

func newFakeDropbox(t *testing.T) *fakeDropbox {
	f := &fakeDropbox{t: t, items: map[string]*fakeDbxItem{}, sessions: map[string][]byte{},
		clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	old := dropboxURL
	dropboxURL = func(host, ns, route string) string { return f.srv.URL + "/" + host + "/2/" + ns + "/" + route }
	t.Cleanup(func() { dropboxURL = old })
	return f
}

func dbxParent(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return ""
}

func (f *fakeDropbox) add(p string, dir bool, content []byte) *fakeDbxItem {
	f.next++
	f.clock = f.clock.Add(time.Minute)
	it := &fakeDbxItem{path: p, id: fmt.Sprintf("id:%d", f.next), dir: dir, content: content, mod: f.clock}
	f.items[strings.ToLower(p)] = it
	return it
}

func (f *fakeDropbox) get(p string) *fakeDbxItem { return f.items[strings.ToLower(p)] }

func (f *fakeDropbox) meta(it *fakeDbxItem) map[string]any {
	name := it.path[strings.LastIndex(it.path, "/")+1:]
	m := map[string]any{"name": name, "id": it.id, "path_lower": strings.ToLower(it.path), "path_display": it.path}
	if it.dir {
		m[".tag"] = "folder"
		return m
	}
	m[".tag"] = "file"
	m["size"] = len(it.content)
	m["client_modified"] = it.mod.Format(dbxTime)
	m["server_modified"] = it.mod.Format(dbxTime)
	m["rev"] = "0123456789abcdef"
	return m
}

// dbxFail answers with a route error, whose summary says what's wrong.
func dbxFail(w http.ResponseWriter, summary string) {
	writeJSON(w, http.StatusConflict, map[string]any{"error_summary": summary, "error": map[string]any{".tag": "other"}})
}

func (f *fakeDropbox) serve(w http.ResponseWriter, r *http.Request) {
	if !checkAuth(w, r) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.throttle > 0 {
		f.throttle--
		w.Header().Set("Retry-After", "0")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error_summary": "too_many_requests/", "error": map[string]any{"reason": map[string]any{".tag": "too_many_requests"}, "retry_after": 0}})
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/2/", 2)
	if len(parts) != 2 {
		http.Error(w, "bad route", 400)
		return
	}
	route := parts[1]
	if strings.HasPrefix(route, "files/") && f.appLacks != "" {
		http.Error(w, fmt.Sprintf(`Error in call to API function "%s": Your app (ID: 1234) is not permitted to access this endpoint because it does not have the required scope '%s'. The owner of the app can enable the scope for the app using the Permissions tab on the App Console.`, route, f.appLacks), http.StatusBadRequest)
		return
	}
	if strings.HasPrefix(route, "files/") && f.tokenLacks != "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error_summary": "missing_scope/",
			"error": map[string]any{".tag": "missing_scope", "required_scope": f.tokenLacks}})
		return
	}
	arg := r.Header.Get("Dropbox-API-Arg")
	var body []byte
	if arg == "" {
		body, _ = io.ReadAll(r.Body)
		arg = string(body)
	} else {
		body, _ = io.ReadAll(r.Body)
	}
	var a struct {
		Path     string `json:"path"`
		FromPath string `json:"from_path"`
		ToPath   string `json:"to_path"`
		Cursor   json.RawMessage
		Mode     struct {
			Tag string `json:".tag"`
		} `json:"mode"`
		Commit *struct {
			Path string `json:"path"`
			Mode struct {
				Tag string `json:".tag"`
			} `json:"mode"`
		} `json:"commit"`
	}
	if arg != "" && arg != "null" {
		if err := json.Unmarshal([]byte(arg), &a); err != nil {
			http.Error(w, "bad argument: "+err.Error(), 400)
			return
		}
	}

	switch route {
	case "users/get_current_account":
		writeJSON(w, 200, map[string]any{
			"account_id": "dbid:1", "email": "user@example.com", "email_verified": true, "disabled": false,
			"name":   map[string]any{"given_name": "U", "surname": "S", "familiar_name": "U", "display_name": "U S", "abbreviated_name": "US"},
			"locale": "en", "referral_link": "", "is_paired": false, "country": "IT",
			"account_type": map[string]any{".tag": "basic"},
			"root_info":    map[string]any{".tag": "user", "root_namespace_id": "1", "home_namespace_id": "1"},
		})
	case "users/get_space_usage":
		writeJSON(w, 200, map[string]any{"used": 1000, "allocation": map[string]any{".tag": "individual", "allocated": 2000000000}})
	case "files/get_metadata":
		it := f.get(a.Path)
		if it == nil {
			dbxFail(w, "path/not_found/")
			return
		}
		writeJSON(w, 200, f.meta(it))
	case "files/list_folder":
		if a.Path != "" {
			if it := f.get(a.Path); it == nil {
				dbxFail(w, "path/not_found/")
				return
			} else if !it.dir {
				dbxFail(w, "path/not_folder/")
				return
			}
		}
		f.listPage(w, strings.ToLower(a.Path), 0)
	case "files/list_folder/continue":
		var c struct {
			Cursor string `json:"cursor"`
		}
		json.Unmarshal([]byte(arg), &c)
		dir, skip, _ := strings.Cut(c.Cursor, "|")
		n, _ := strconv.Atoi(skip)
		f.listPage(w, dir, n)
	case "files/create_folder_v2":
		if f.get(a.Path) != nil {
			dbxFail(w, "path/conflict/folder/")
			return
		}
		if p := dbxParent(a.Path); p != "" && f.get(p) == nil {
			f.add(p, true, nil) // Dropbox creates missing parents
		}
		writeJSON(w, 200, map[string]any{"metadata": f.meta(f.add(a.Path, true, nil))})
	case "files/delete_v2":
		it := f.get(a.Path)
		if it == nil {
			dbxFail(w, "path_lookup/not_found/")
			return
		}
		lower := strings.ToLower(a.Path)
		for k := range f.items {
			if k == lower || strings.HasPrefix(k, lower+"/") {
				delete(f.items, k)
			}
		}
		writeJSON(w, 200, map[string]any{"metadata": f.meta(it)})
	case "files/move_v2":
		src := f.get(a.FromPath)
		if src == nil {
			dbxFail(w, "from_lookup/not_found/")
			return
		}
		if dst := f.get(a.ToPath); dst != nil && dst != src {
			dbxFail(w, "to/conflict/file/")
			return
		}
		from, to := strings.ToLower(a.FromPath), a.ToPath
		moved := map[string]*fakeDbxItem{}
		for k, it := range f.items {
			if k == from || strings.HasPrefix(k, from+"/") {
				delete(f.items, k)
				it.path = to + it.path[len(from):]
				moved[strings.ToLower(it.path)] = it
			}
		}
		for k, it := range moved {
			f.items[k] = it
		}
		writeJSON(w, 200, map[string]any{"metadata": f.meta(src)})
	case "files/upload":
		f.commit(w, a.Path, a.Mode.Tag, body)
	case "files/upload_session/start":
		f.next++
		id := fmt.Sprintf("sess%d", f.next)
		f.sessions[id] = body
		writeJSON(w, 200, map[string]any{"session_id": id})
	case "files/upload_session/append_v2", "files/upload_session/finish":
		var c struct {
			Cursor struct {
				SessionID string `json:"session_id"`
				Offset    int    `json:"offset"`
			} `json:"cursor"`
		}
		json.Unmarshal([]byte(arg), &c)
		data, ok := f.sessions[c.Cursor.SessionID]
		if !ok || c.Cursor.Offset != len(data) {
			dbxFail(w, "lookup_failed/incorrect_offset/")
			return
		}
		data = append(data, body...)
		f.sessions[c.Cursor.SessionID] = data
		if route == "files/upload_session/finish" {
			delete(f.sessions, c.Cursor.SessionID)
			f.commit(w, a.Commit.Path, a.Commit.Mode.Tag, data)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("null"))
	case "files/download":
		it := f.get(a.Path)
		switch {
		case it == nil:
			dbxFail(w, "path/not_found/")
			return
		case it.dir:
			dbxFail(w, "path/not_file/")
			return
		}
		meta, _ := json.Marshal(f.meta(it))
		w.Header().Set("Dropbox-API-Result", string(meta))
		serveRange(w, r, it.content)
	default:
		http.Error(w, "unknown route "+route, 400)
	}
}

func (f *fakeDropbox) commit(w http.ResponseWriter, p, mode string, data []byte) {
	if it := f.get(p); it != nil {
		if it.dir || mode == "add" {
			dbxFail(w, "path/conflict/file/")
			return
		}
		it.content = data
		writeJSON(w, 200, f.meta(it))
		return
	}
	writeJSON(w, 200, f.meta(f.add(p, false, data)))
}

func (f *fakeDropbox) listPage(w http.ResponseWriter, dir string, skip int) {
	var kids []*fakeDbxItem
	for k, it := range f.items {
		if dbxParent(k) == dir {
			kids = append(kids, it)
		}
	}
	sort.Slice(kids, func(i, j int) bool { return kids[i].path < kids[j].path })
	const page = 2
	entries := []any{}
	for i := skip; i < len(kids) && i < skip+page; i++ {
		entries = append(entries, f.meta(kids[i]))
	}
	writeJSON(w, 200, map[string]any{
		"entries": entries, "has_more": skip+page < len(kids), "cursor": fmt.Sprintf("%s|%d", dir, skip+page),
	})
}
