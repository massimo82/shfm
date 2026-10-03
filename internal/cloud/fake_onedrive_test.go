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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGraph is an in-memory OneDrive serving the part of Microsoft Graph
// the oneDrive backend uses: the account's drive ("ME") and other users'
// drives, links (remoteItem) between them, which path addressing doesn't
// go through, as on Graph, and Microsoft Search. Like OneDrive, it ignores
// case in names.
type fakeGraph struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	items     map[string]*fakeGraphItem
	sessions  map[string]*fakeGraphSession
	next      int
	clock     time.Time
	pageSize  int
	driveType string // the account's: "personal" or "business"

	tokenOnPreauth bool // a pre-authenticated URL got the account's token
	searches       int
}

type fakeGraphItem struct {
	id, drive, name, parent string // parent "" for a drive's root
	dir                     bool
	content                 []byte
	mod, fsMod              time.Time
	link                    *fakeGraphItem // a remoteItem: the item it opens
	shared                  bool           // shared with the account (found by Search)
}

type fakeGraphSession struct {
	parent *fakeGraphItem
	name   string
	data   []byte
}

const fakeMyDrive = "ME"

func newFakeGraph(t *testing.T) *fakeGraph {
	f := &fakeGraph{
		t:         t,
		items:     map[string]*fakeGraphItem{},
		sessions:  map[string]*fakeGraphSession{},
		clock:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		pageSize:  3,
		driveType: "personal",
	}
	f.addDrive(fakeMyDrive)
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	old := graphAPI
	graphAPI = f.srv.URL + "/v1.0"
	t.Cleanup(func() { graphAPI = old })
	return f
}

// addDrive makes a drive (another user's OneDrive), returning its root.
func (f *fakeGraph) addDrive(id string) *fakeGraphItem {
	root := &fakeGraphItem{id: id + "-ROOT", drive: id, dir: true}
	f.items[root.id] = root
	return root
}

func (f *fakeGraph) root(drive string) *fakeGraphItem { return f.items[drive+"-ROOT"] }

func graphFail(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": code}})
}

func (f *fakeGraph) children(parent *fakeGraphItem) []*fakeGraphItem {
	var out []*fakeGraphItem
	for _, it := range f.items {
		if it.parent == parent.id && it.drive == parent.drive {
			out = append(out, it)
		}
	}
	return out
}

func (f *fakeGraph) child(parent *fakeGraphItem, name string) *fakeGraphItem {
	for _, it := range f.children(parent) {
		if strings.EqualFold(it.name, name) {
			return it
		}
	}
	return nil
}

// walk resolves rel from base, never through a link.
func (f *fakeGraph) walk(base *fakeGraphItem, rel string) *fakeGraphItem {
	it := base
	for _, seg := range strings.Split(strings.Trim(rel, "/"), "/") {
		if seg == "" {
			continue
		}
		if it.link != nil || !it.dir {
			return nil
		}
		if it = f.child(it, seg); it == nil {
			return nil
		}
	}
	return it
}

func (f *fakeGraph) add(parent *fakeGraphItem, name string, dir bool, content []byte) *fakeGraphItem {
	f.next++
	f.clock = f.clock.Add(time.Minute)
	it := &fakeGraphItem{id: fmt.Sprintf("ID%d", f.next), drive: parent.drive, name: name, parent: parent.id,
		dir: dir, content: content, mod: f.clock, fsMod: f.clock}
	f.items[it.id] = it
	return it
}

// addLink adds to parent a link opening target (a shared folder added to
// My files).
func (f *fakeGraph) addLink(parent *fakeGraphItem, target *fakeGraphItem) *fakeGraphItem {
	l := f.add(parent, target.name, false, nil)
	l.link = target
	return l
}

func (f *fakeGraph) json(it *fakeGraphItem) map[string]any {
	m := map[string]any{
		"id": it.id, "name": it.name, "lastModifiedDateTime": it.mod.Format(time.RFC3339),
		"fileSystemInfo":  map[string]any{"lastModifiedDateTime": it.fsMod.Format(time.RFC3339)},
		"parentReference": map[string]any{"driveId": it.drive, "id": it.parent},
	}
	switch {
	case it.link != nil:
		r := map[string]any{"id": it.link.id, "size": len(it.link.content), "parentReference": map[string]any{"driveId": it.link.drive}}
		if it.link.dir {
			r["folder"] = map[string]any{}
		}
		m["remoteItem"] = r
	case it.dir:
		m["folder"] = map[string]any{"childCount": len(f.children(it))}
	default:
		m["size"] = len(it.content)
		m["file"] = map[string]any{}
		m["@microsoft.graph.downloadUrl"] = f.srv.URL + "/download/" + it.id
	}
	return m
}

func (f *fakeGraph) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw := r.URL.EscapedPath()

	// Pre-authenticated URLs: they must not get the account's token.
	if strings.HasPrefix(raw, "/download/") || strings.HasPrefix(raw, "/upload/") {
		if r.Header.Get("Authorization") != "" {
			f.tokenOnPreauth = true
		}
		if strings.HasPrefix(raw, "/download/") {
			it, ok := f.items[strings.TrimPrefix(raw, "/download/")]
			if !ok {
				graphFail(w, 404, "itemNotFound")
				return
			}
			serveRange(w, r, it.content)
			return
		}
		f.uploadChunk(w, r, strings.TrimPrefix(raw, "/upload/"))
		return
	}
	if !checkAuth(w, r) {
		return
	}
	switch raw {
	case "/v1.0/me":
		writeJSON(w, 200, map[string]any{"userPrincipalName": "user@outlook.com"})
		return
	case "/v1.0/me/drive":
		writeJSON(w, 200, map[string]any{
			"id": fakeMyDrive, "driveType": f.driveType,
			"webUrl": "https://contoso-my.sharepoint.com/personal/user_contoso_com/Documents",
			"quota":  map[string]any{"total": 5000000000, "remaining": 4000000000},
		})
		return
	case "/v1.0/search/query":
		f.search(w, r)
		return
	}

	// The item the path is relative to.
	var base *fakeGraphItem
	var rest string
	switch {
	case strings.HasPrefix(raw, "/v1.0/me/drive/root"):
		base, rest = f.root(fakeMyDrive), strings.TrimPrefix(raw, "/v1.0/me/drive/root")
	case strings.HasPrefix(raw, "/v1.0/drives/"):
		drive, after, _ := strings.Cut(strings.TrimPrefix(raw, "/v1.0/drives/"), "/")
		if !strings.HasPrefix(after, "items/") {
			graphFail(w, 400, "invalidRequest")
			return
		}
		parts := []string{drive}
		id := strings.TrimPrefix(after, "items/")
		id, rest, _ = strings.Cut(id, ":")
		if i := strings.Index(id, "/"); i >= 0 {
			id, rest = id[:i], id[i:]
		} else if rest != "" {
			rest = ":" + rest
		}
		id, _ = url.PathUnescape(id)
		base = f.items[id]
		if base == nil || base.drive != parts[0] {
			graphFail(w, 404, "itemNotFound")
			return
		}
	default:
		graphFail(w, 400, "invalidRequest")
		return
	}
	relPath, suffix := "", rest
	if strings.HasPrefix(rest, ":") {
		rest = rest[1:]
		if i := strings.Index(rest, ":"); i >= 0 {
			relPath, suffix = rest[:i], rest[i+1:]
		} else {
			relPath, suffix = rest, ""
		}
	}
	decoded, err := url.PathUnescape(relPath)
	if err != nil {
		graphFail(w, 400, "invalidRequest")
		return
	}
	it := f.walk(base, decoded)
	q := r.URL.Query()
	parentOfPath := func() *fakeGraphItem { return f.walk(base, parentOf(decoded)) }

	switch {
	case suffix == "" && r.Method == http.MethodGet:
		if it == nil {
			graphFail(w, 404, "itemNotFound")
			return
		}
		writeJSON(w, 200, f.json(it))
	case suffix == "" && r.Method == http.MethodDelete:
		if it == nil {
			graphFail(w, 404, "itemNotFound")
			return
		}
		f.delete(it)
		w.WriteHeader(http.StatusNoContent)
	case suffix == "" && r.Method == http.MethodPatch:
		if it == nil {
			graphFail(w, 404, "itemNotFound")
			return
		}
		f.patch(w, r, it)
	case suffix == "/children" && r.Method == http.MethodGet:
		if it == nil {
			graphFail(w, 404, "itemNotFound")
			return
		}
		if !it.dir || it.link != nil {
			graphFail(w, 400, "invalidRequest")
			return
		}
		skip, _ := strconv.Atoi(q.Get("skip"))
		f.listChildren(w, it, skip)
	case suffix == "/children" && r.Method == http.MethodPost:
		if it == nil || !it.dir || it.link != nil {
			graphFail(w, 404, "itemNotFound")
			return
		}
		var body struct {
			Name string `json:"name"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if f.child(it, body.Name) != nil {
			graphFail(w, 409, "nameAlreadyExists")
			return
		}
		writeJSON(w, 201, f.json(f.add(it, body.Name, true, nil)))
	case suffix == "/content" && r.Method == http.MethodPut:
		parent := parentOfPath()
		if parent == nil || !parent.dir || parent.link != nil {
			graphFail(w, 404, "itemNotFound")
			return
		}
		data, _ := io.ReadAll(r.Body)
		if it != nil {
			if q.Get("@microsoft.graph.conflictBehavior") == "fail" || it.dir {
				graphFail(w, 409, "nameAlreadyExists")
				return
			}
			it.content = data
			f.clock = f.clock.Add(time.Minute)
			it.mod, it.fsMod = f.clock, f.clock
			writeJSON(w, 200, f.json(it))
			return
		}
		writeJSON(w, 201, f.json(f.add(parent, baseOf(decoded), false, data)))
	case suffix == "/createUploadSession" && r.Method == http.MethodPost:
		parent := parentOfPath()
		if parent == nil || !parent.dir || parent.link != nil {
			graphFail(w, 404, "itemNotFound")
			return
		}
		f.next++
		id := fmt.Sprintf("S%d", f.next)
		f.sessions[id] = &fakeGraphSession{parent: parent, name: baseOf(decoded)}
		writeJSON(w, 200, map[string]any{"uploadUrl": f.srv.URL + "/upload/" + id})
	default:
		graphFail(w, 400, "invalidRequest")
	}
}

func parentOf(p string) string {
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "/"
}

func baseOf(p string) string { return p[strings.LastIndex(p, "/")+1:] }

func (f *fakeGraph) listChildren(w http.ResponseWriter, it *fakeGraphItem, skip int) {
	kids := f.children(it)
	value := []any{}
	for i := skip; i < len(kids) && i < skip+f.pageSize; i++ {
		value = append(value, f.json(kids[i]))
	}
	resp := map[string]any{"value": value}
	if skip+f.pageSize < len(kids) {
		resp["@odata.nextLink"] = fmt.Sprintf("%s/v1.0/drives/%s/items/%s/children?skip=%d", f.srv.URL, it.drive, it.id, skip+f.pageSize)
	}
	writeJSON(w, 200, resp)
}

func (f *fakeGraph) delete(it *fakeGraphItem) {
	for _, c := range f.children(it) {
		f.delete(c)
	}
	delete(f.items, it.id)
}

func (f *fakeGraph) patch(w http.ResponseWriter, r *http.Request, it *fakeGraphItem) {
	var body struct {
		Name            *string `json:"name"`
		ParentReference *struct {
			ID string `json:"id"`
		} `json:"parentReference"`
		FileSystemInfo *struct {
			LastModifiedDateTime time.Time `json:"lastModifiedDateTime"`
		} `json:"fileSystemInfo"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	parent, name := f.items[it.parent], it.name
	if body.ParentReference != nil {
		parent = f.items[body.ParentReference.ID]
		if parent == nil || parent.drive != it.drive {
			graphFail(w, 400, "invalidRequest") // Graph doesn't move between drives
			return
		}
	}
	if body.Name != nil {
		name = *body.Name
	}
	if parent != nil {
		if other := f.child(parent, name); other != nil && other != it {
			graphFail(w, 409, "nameAlreadyExists")
			return
		}
		it.parent = parent.id
	}
	it.name = name
	if body.FileSystemInfo != nil {
		it.fsMod = body.FileSystemInfo.LastModifiedDateTime
	}
	writeJSON(w, 200, f.json(it))
}

func (f *fakeGraph) uploadChunk(w http.ResponseWriter, r *http.Request, id string) {
	s, ok := f.sessions[id]
	if !ok {
		graphFail(w, 404, "itemNotFound")
		return
	}
	if r.Method == http.MethodDelete {
		delete(f.sessions, id)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var start, end, total int
	if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil {
		graphFail(w, 400, "invalidRange")
		return
	}
	data, _ := io.ReadAll(r.Body)
	if start != len(s.data) || end-start+1 != len(data) || (end+1 < total && len(data)%(320<<10) != 0 && len(data) != oneDriveChunk) {
		graphFail(w, 416, "invalidRange")
		return
	}
	s.data = append(s.data, data...)
	if len(s.data) < total {
		writeJSON(w, 202, map[string]any{"nextExpectedRanges": []string{fmt.Sprintf("%d-", len(s.data))}})
		return
	}
	delete(f.sessions, id)
	if it := f.child(s.parent, s.name); it != nil {
		it.content = s.data
		writeJSON(w, 200, f.json(it))
		return
	}
	writeJSON(w, 201, f.json(f.add(s.parent, s.name, false, s.data)))
}

// search answers Microsoft Search with every item of other users'
// OneDrives the account can reach: the shared ones and what's inside the
// shared folders.
func (f *fakeGraph) search(w http.ResponseWriter, r *http.Request) {
	f.searches++
	if f.driveType != "business" {
		graphFail(w, 400, "BadRequest") // as for personal accounts
		return
	}
	var body struct {
		Requests []struct {
			EntityTypes []string `json:"entityTypes"`
			Query       struct {
				QueryString string `json:"queryString"`
			} `json:"query"`
			From int `json:"from"`
			Size int `json:"size"`
		} `json:"requests"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if len(body.Requests) != 1 || body.Requests[0].Query.QueryString != `path:"https://contoso-my.sharepoint.com/personal/"` ||
		len(body.Requests[0].EntityTypes) != 1 || body.Requests[0].EntityTypes[0] != "driveItem" {
		graphFail(w, 400, "BadRequest")
		return
	}
	var reachable func(it *fakeGraphItem) bool
	reachable = func(it *fakeGraphItem) bool {
		if it == nil || it.drive == fakeMyDrive {
			return false
		}
		return it.shared || reachable(f.items[it.parent])
	}
	var all []any
	for _, it := range f.items {
		if reachable(it) {
			all = append(all, map[string]any{"resource": map[string]any{
				"@odata.type": "#microsoft.graph.driveItem", "id": it.id, "name": it.name, "size": len(it.content),
				"lastModifiedDateTime": it.mod.Format(time.RFC3339),
				"parentReference":      map[string]any{"driveId": it.drive, "id": it.parent},
			}})
		}
	}
	req := body.Requests[0]
	end := min(req.From+req.Size, len(all))
	hits := []any{}
	if req.From < len(all) {
		hits = all[req.From:end]
	}
	writeJSON(w, 200, map[string]any{"value": []any{map[string]any{"hitsContainers": []any{
		map[string]any{"hits": hits, "total": len(all), "moreResultsAvailable": end < len(all)},
	}}}})
}
