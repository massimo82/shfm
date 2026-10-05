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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"shfm/internal/vfs"
)

// Microsoft OneDrive, through the Microsoft Graph REST API v1.0
// (https://learn.microsoft.com/graph/api/resources/onedrive) — plain HTTP
// and JSON: Microsoft's Go SDK is a very large generated library. Items
// are addressed by path, relative to the drive's root or to an item
// ("/me/drive/root:/a/b:", "/drives/{drive}/items/{item}:/c:").
//
// The account's root holds "My files", the account's own drive, and, for
// work or school accounts, "Shared": the files and folders other people
// shared from their OneDrive, found through Microsoft Search — Graph's
// sharedWithMe endpoint is deprecated (degraded now, then retired), and
// personal accounts have no replacement for it. On any account, a shared
// folder added to "My files" from OneDrive's website is a link
// (remoteItem) there, which opens the shared folder: operations on the
// link's own entry (rename, remove) act on the link, never on the folder
// it points to. "My files", "Shared" and the items in "Shared" can't be
// renamed, moved or removed from shfm.
//
// Downloads and upload sessions go to pre-authenticated URLs Graph hands
// out, which must not get the account's token. Upload sessions need the
// file's size up front: the source's FS is a vfs.SizedCreator, and content
// of unknown size is spooled first. OneNote notebooks (packages) are
// listed as folders. Moving between drives (into a shared folder) isn't a
// Graph move: it's copied and deleted instead.

var (
	graphAPI = "https://graph.microsoft.com/v1.0"
	// oneDriveEndpoint is a variable so that tests can redirect it.
	oneDriveEndpoint = oauth2.Endpoint{
		AuthURL:   "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		TokenURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		AuthStyle: oauth2.AuthStyleInParams,
	}
)

// Variables so that tests can make uploads of several chunks small.
var (
	// oneDriveSimpleMax is the largest file uploaded in a single request;
	// larger ones go through an upload session.
	oneDriveSimpleMax int64 = 4 << 20
	// oneDriveChunk is the size of an upload session's request: a
	// multiple of 320 KiB, as Graph requires.
	oneDriveChunk = 32 * 320 << 10
)

// OneDrive's places, the folders at the account's root.
const (
	oneDriveMyFiles = "My files"
	oneDriveShared  = "Shared"
)

// oneDriveSearchMax is the most search results looked at for "Shared".
const oneDriveSearchMax = 1000

func init() {
	register(&provider{
		info: ProviderInfo{
			ID: OneDrive, Name: "Microsoft OneDrive", DefaultClientID: oneDriveClientID,
			RedirectURI: "http://localhost (any port, \"Mobile and desktop applications\" platform)",
		},
		endpoint:     func() oauth2.Endpoint { return oneDriveEndpoint },
		scopes:       []string{"Files.ReadWrite.All", "User.Read", "offline_access"},
		redirectHost: "localhost",
		newBackend:   func(c *http.Client, _ oauth2.TokenSource) backend { return newOneDrive(c) },
		caps:         caps{streamUpload: false, modTime: true, home: "/" + oneDriveMyFiles},
	})
}

type oneDrive struct {
	client *http.Client

	mu       sync.Mutex
	me       *graphDrive          // the account's drive, once known
	links    map[string]graphRef  // link path (in My files) -> the item it opens
	plain    map[string]time.Time // My files folders found not to be links, and when
	shared   []graphShared        // "Shared", as of sharedAt
	sharedAt time.Time
}

func newOneDrive(c *http.Client) *oneDrive {
	return &oneDrive{client: c, links: map[string]graphRef{}, plain: map[string]time.Time{}}
}

type graphRef struct {
	driveID, id string
}

type graphShared struct {
	name string
	ref  graphRef
	item graphItem
}

type graphDrive struct {
	ID        string `json:"id"`
	DriveType string `json:"driveType"` // "personal", "business" or "documentLibrary"
	WebURL    string `json:"webUrl"`
}

type graphParentRef struct {
	DriveID string `json:"driveId"`
	ID      string `json:"id"`
}

type graphItem struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Size                 int64           `json:"size"`
	LastModifiedDateTime time.Time       `json:"lastModifiedDateTime"`
	Folder               *struct{}       `json:"folder,omitempty"`
	Package              *struct{}       `json:"package,omitempty"`
	ParentReference      *graphParentRef `json:"parentReference,omitempty"`
	FileSystemInfo       *struct {
		LastModifiedDateTime time.Time `json:"lastModifiedDateTime"`
	} `json:"fileSystemInfo,omitempty"`
	DownloadURL string `json:"@microsoft.graph.downloadUrl"`

	// RemoteItem makes the item a link to an item of another drive (a
	// shared folder added to My files).
	RemoteItem *struct {
		ID              string          `json:"id"`
		Size            int64           `json:"size"`
		Folder          *struct{}       `json:"folder,omitempty"`
		Package         *struct{}       `json:"package,omitempty"`
		ParentReference *graphParentRef `json:"parentReference,omitempty"`
	} `json:"remoteItem,omitempty"`
}

func (it graphItem) isDir() bool {
	if r := it.RemoteItem; r != nil {
		return r.Folder != nil || r.Package != nil
	}
	return it.Folder != nil || it.Package != nil
}

func (it graphItem) driveID() string {
	if it.ParentReference != nil {
		return it.ParentReference.DriveID
	}
	return ""
}

// target is the item a link opens.
func (it graphItem) target() (graphRef, bool) {
	r := it.RemoteItem
	if r == nil || r.ParentReference == nil {
		return graphRef{}, false
	}
	return graphRef{driveID: r.ParentReference.DriveID, id: r.ID}, true
}

func (it graphItem) entry() vfs.Entry {
	mod := it.LastModifiedDateTime
	if it.FileSystemInfo != nil && !it.FileSystemInfo.LastModifiedDateTime.IsZero() {
		mod = it.FileSystemInfo.LastModifiedDateTime // the file's own, as uploaded
	}
	var e vfs.Entry
	switch {
	case it.isDir():
		e = dirEntry(it.Name, mod)
	case it.RemoteItem != nil:
		e = fileEntry(it.Name, it.RemoteItem.Size, mod)
	default:
		e = fileEntry(it.Name, it.Size, mod)
	}
	e.IsSymlink = it.RemoteItem != nil
	return e
}

var (
	errOneDriveFixed = &APIError{Status: http.StatusForbidden, kind: os.ErrPermission,
		Message: "OneDrive's places can't be renamed, moved or removed"}
	errOneDriveVirtual = &APIError{Status: http.StatusForbidden, kind: os.ErrPermission,
		Message: "nothing can be created here, only inside folders"}
)

// graphEscape escapes a path for Graph's path addressing, where ':' ends
// the path.
func graphEscape(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(url.PathEscape(s), ":", "%3A")
	}
	return strings.Join(segs, "/")
}

// graphLoc is where an item is for Graph: rel, a path relative to base, an
// item or a drive's root ("/me/drive/root", "/drives/{d}/items/{i}").
type graphLoc struct {
	base, rel string
}

// url addresses the item, plus suffix ("/children", "/content"...).
func (l graphLoc) url(suffix string) string {
	if l.rel == "/" || l.rel == "" {
		return graphAPI + l.base + suffix
	}
	if suffix == "" {
		return graphAPI + l.base + ":" + graphEscape(l.rel)
	}
	return graphAPI + l.base + ":" + graphEscape(l.rel) + ":" + suffix
}

func refBase(r graphRef) string {
	return "/drives/" + url.PathEscape(r.driveID) + "/items/" + url.PathEscape(r.id)
}

func graphParseErr(status int, body []byte) error {
	var b struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &b)
	return &APIError{Status: status, Code: b.Error.Code, Message: b.Error.Message, kind: kindOfStatus(status)}
}

// call sends a JSON request (body: nil or a value to encode) and decodes
// the answer into out (nil to discard it).
func (o *oneDrive) call(ctx context.Context, method, u string, body, out any) error {
	var data []byte
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return err
		}
	}
	resp, err := doRetry(ctx, o.client, defaultRetry, func() (*http.Request, error) {
		var r io.Reader
		if data != nil {
			r = bytes.NewReader(data)
		}
		req, err := http.NewRequest(method, u, r)
		if err == nil && data != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	}, graphParseErr)
	if err != nil {
		return err
	}
	return decodeJSON(resp, out)
}

// drive returns the account's own drive.
func (o *oneDrive) drive(ctx context.Context) (graphDrive, error) {
	o.mu.Lock()
	if o.me != nil {
		me := *o.me
		o.mu.Unlock()
		return me, nil
	}
	o.mu.Unlock()
	var me graphDrive
	if err := o.call(ctx, http.MethodGet, graphAPI+"/me/drive?$select=id,driveType,webUrl", nil, &me); err != nil {
		return graphDrive{}, err
	}
	o.mu.Lock()
	o.me = &me
	o.mu.Unlock()
	return me, nil
}

// business tells whether it's a work or school account, the only kind
// with a "Shared" place.
func (o *oneDrive) business(ctx context.Context) (bool, error) {
	me, err := o.drive(ctx)
	return me.DriveType == "business", err
}

// --- places and links ---------------------------------------------------------

// place describes the paths that aren't an item in a folder: the root, My
// files, Shared and the items in it. ok is false for any other path.
type oneDrivePlace struct {
	loc     graphLoc
	entry   vfs.Entry
	virtual bool // no folder behind it: nothing can be created in it
}

func (o *oneDrive) place(ctx context.Context, p string) (pl oneDrivePlace, ok bool, err error) {
	if p == "/" {
		return oneDrivePlace{entry: dirEntry("/", time.Time{}), virtual: true}, true, nil
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	switch {
	case segs[0] == oneDriveMyFiles:
		if len(segs) == 1 {
			return oneDrivePlace{loc: graphLoc{base: "/me/drive/root", rel: "/"}, entry: dirEntry(oneDriveMyFiles, time.Time{})}, true, nil
		}
		return oneDrivePlace{}, false, nil
	case segs[0] != oneDriveShared:
		return oneDrivePlace{}, true, os.ErrNotExist
	}
	if biz, err := o.business(ctx); err != nil {
		return oneDrivePlace{}, true, err
	} else if !biz {
		return oneDrivePlace{}, true, os.ErrNotExist
	}
	if len(segs) == 1 {
		return oneDrivePlace{entry: dirEntry(oneDriveShared, time.Time{}), virtual: true}, true, nil
	}
	items, err := o.sharedItems(ctx)
	if err != nil {
		return oneDrivePlace{}, true, err
	}
	for _, sh := range items {
		if sh.name == segs[1] {
			pl := oneDrivePlace{loc: graphLoc{base: refBase(sh.ref), rel: "/" + strings.Join(segs[2:], "/")}}
			if len(segs) == 2 {
				pl.entry = sh.item.entry()
				pl.entry.Name = sh.name
				return pl, true, nil
			}
			return pl, false, nil // inside a shared folder: an ordinary item
		}
	}
	return oneDrivePlace{}, true, os.ErrNotExist
}

// locate finds where item p (below My files or a shared item) is. With
// self, a link at p itself is the link's own entry, not what it opens.
func (o *oneDrive) locate(ctx context.Context, p string, self bool) (graphLoc, error) {
	pl, ok, err := o.place(ctx, p)
	if err != nil {
		return graphLoc{}, err
	}
	if ok {
		if pl.virtual {
			return graphLoc{}, errOneDriveVirtual
		}
		return pl.loc, nil
	}
	if pl.loc.base != "" { // inside a shared item
		return pl.loc, nil
	}
	// Below My files: relative to the deepest link on the path, if any.
	rel := strings.TrimPrefix(p, "/"+oneDriveMyFiles)
	o.mu.Lock()
	defer o.mu.Unlock()
	best := ""
	for lp := range o.links {
		if (lp == p && !self) || strings.HasPrefix(p, lp+"/") {
			if len(lp) > len(best) {
				best = lp
			}
		}
	}
	if best == "" {
		return graphLoc{base: "/me/drive/root", rel: rel}, nil
	}
	return graphLoc{base: refBase(o.links[best]), rel: strings.TrimPrefix(p, best)}, nil
}

var oneDrivePlainTTL = 30 * time.Second

// findLinks looks along p's path, below My files, for links not known yet
// (Graph's path addressing doesn't go through a link): it reports whether
// it found any, so that the operation that failed can be tried again.
func (o *oneDrive) findLinks(ctx context.Context, p string, self bool) bool {
	if !strings.HasPrefix(p, "/"+oneDriveMyFiles+"/") {
		return false
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"+oneDriveMyFiles+"/"), "/")
	if self {
		segs = segs[:len(segs)-1]
	}
	found := false
	cur := "/" + oneDriveMyFiles
	for _, seg := range segs {
		cur += "/" + seg
		o.mu.Lock()
		_, known := o.links[cur]
		checked, wasPlain := o.plain[cur]
		o.mu.Unlock()
		if known || (wasPlain && time.Since(checked) < oneDrivePlainTTL) {
			continue
		}
		l, err := o.locate(ctx, cur, true)
		if err != nil {
			return found
		}
		var it graphItem
		if err := o.call(ctx, http.MethodGet, l.url(""), nil, &it); err != nil {
			return found
		}
		o.mu.Lock()
		if ref, ok := it.target(); ok {
			o.links[cur] = ref
			found = true
		} else {
			o.plain[cur] = time.Now()
		}
		o.mu.Unlock()
	}
	return found
}

// forget drops the links at and below p.
func (o *oneDrive) forget(p string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for k := range o.links {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(o.links, k)
		}
	}
	for k := range o.plain {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(o.plain, k)
		}
	}
}

// at runs op on where p is, and again if it turns out to be behind a link
// not known yet.
func (o *oneDrive) at(ctx context.Context, p string, self bool, op func(l graphLoc) error) error {
	l, err := o.locate(ctx, p, self)
	if err != nil {
		return err
	}
	err = op(l)
	if errors.Is(err, os.ErrNotExist) && o.findLinks(ctx, p, self) {
		if l, err = o.locate(ctx, p, self); err == nil {
			err = op(l)
		}
	}
	return err
}

func (o *oneDrive) item(ctx context.Context, p string, self bool) (graphItem, error) {
	var it graphItem
	err := o.at(ctx, p, self, func(l graphLoc) error {
		it = graphItem{}
		return o.call(ctx, http.MethodGet, l.url(""), nil, &it)
	})
	if err == nil && strings.HasPrefix(p, "/"+oneDriveMyFiles+"/") {
		if ref, ok := it.target(); ok {
			o.mu.Lock()
			o.links[p] = ref
			o.mu.Unlock()
		}
	}
	return it, err
}

// sharedItems lists what other people shared with a work or school
// account from their OneDrive: Microsoft Search's driveItems in other
// users' OneDrive sites, minus those inside a folder found too (the
// contents of a shared folder are reached through it).
func (o *oneDrive) sharedItems(ctx context.Context) ([]graphShared, error) {
	o.mu.Lock()
	if o.shared != nil && time.Since(o.sharedAt) < oneDrivePlainTTL {
		items := o.shared
		o.mu.Unlock()
		return items, nil
	}
	o.mu.Unlock()
	me, err := o.drive(ctx)
	if err != nil {
		return nil, err
	}
	// Other users' OneDrives are sites under https://<tenant>-my.sharepoint.com/personal/.
	prefix, _, ok := strings.Cut(me.WebURL, "/personal/")
	if !ok {
		return nil, fmt.Errorf("can't tell where this account's organization keeps OneDrives (%q)", me.WebURL)
	}
	type hit struct {
		Resource graphItem `json:"resource"`
	}
	var hits []hit
	for from := 0; from < oneDriveSearchMax; {
		req := map[string]any{"requests": []any{map[string]any{
			"entityTypes": []string{"driveItem"},
			"query":       map[string]any{"queryString": fmt.Sprintf("path:\"%s/personal/\"", prefix)},
			"from":        from, "size": 200,
		}}}
		var resp struct {
			Value []struct {
				HitsContainers []struct {
					Hits                 []hit `json:"hits"`
					MoreResultsAvailable bool  `json:"moreResultsAvailable"`
				} `json:"hitsContainers"`
			} `json:"value"`
		}
		if err := o.call(ctx, http.MethodPost, graphAPI+"/search/query", req, &resp); err != nil {
			return nil, err
		}
		more := false
		for _, v := range resp.Value {
			for _, c := range v.HitsContainers {
				hits = append(hits, c.Hits...)
				more = more || c.MoreResultsAvailable
			}
		}
		if !more {
			break
		}
		from += 200
	}

	ids := map[string]bool{}
	for _, h := range hits {
		ids[h.Resource.ID] = true
	}
	items := []graphShared{}
	seen := map[string]int{}
	for _, h := range hits {
		r := h.Resource
		if r.ParentReference == nil || r.ParentReference.DriveID == me.ID || ids[r.ParentReference.ID] {
			continue
		}
		ref := graphRef{driveID: r.ParentReference.DriveID, id: r.ID}
		var it graphItem
		if err := o.call(ctx, http.MethodGet, graphAPI+refBase(ref), nil, &it); err != nil {
			continue // gone, or no longer shared
		}
		name := it.Name
		if seen[name]++; seen[name] > 1 {
			ext := path.Ext(name)
			name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), seen[name], ext)
		}
		items = append(items, graphShared{name: name, ref: ref, item: it})
	}
	o.mu.Lock()
	o.shared, o.sharedAt = items, time.Now()
	o.mu.Unlock()
	return items, nil
}

// --- backend -------------------------------------------------------------------

func (o *oneDrive) stat(ctx context.Context, p string) (vfs.Entry, error) {
	if pl, ok, err := o.place(ctx, p); ok {
		return pl.entry, err
	}
	it, err := o.item(ctx, p, true)
	return it.entry(), err
}

func (o *oneDrive) list(ctx context.Context, p string) ([]vfs.Entry, error) {
	switch p {
	case "/":
		entries := []vfs.Entry{dirEntry(oneDriveMyFiles, time.Time{})}
		if biz, err := o.business(ctx); err != nil {
			return nil, err
		} else if biz {
			entries = append(entries, dirEntry(oneDriveShared, time.Time{}))
		}
		return entries, nil
	case "/" + oneDriveShared:
		if _, _, err := o.place(ctx, p); err != nil {
			return nil, err
		}
		items, err := o.sharedItems(ctx)
		if err != nil {
			return nil, err
		}
		entries := make([]vfs.Entry, 0, len(items))
		for _, sh := range items {
			e := sh.item.entry()
			e.Name = sh.name
			entries = append(entries, e)
		}
		return entries, nil
	}

	var entries []vfs.Entry
	err := o.at(ctx, p, false, func(l graphLoc) error {
		entries = nil
		u := l.url("/children") + "?$top=1000"
		for u != "" {
			var page struct {
				Value    []graphItem `json:"value"`
				NextLink string      `json:"@odata.nextLink"`
			}
			if err := o.call(ctx, http.MethodGet, u, nil, &page); err != nil {
				return err
			}
			for _, it := range page.Value {
				if ref, ok := it.target(); ok && strings.HasPrefix(p, "/"+oneDriveMyFiles) {
					o.mu.Lock()
					o.links[path.Join(p, it.Name)] = ref
					o.mu.Unlock()
				}
				entries = append(entries, it.entry())
			}
			u = page.NextLink
		}
		return nil
	})
	if err != nil {
		// Listing a file's children: say what's wrong.
		if !errors.Is(err, os.ErrNotExist) {
			if it, serr := o.item(ctx, p, false); serr == nil && !it.isDir() {
				return nil, errNotDir
			}
		}
		return nil, err
	}
	if entries == nil {
		entries = []vfs.Entry{}
	}
	return entries, nil
}

// fixedOrVirtual reports why p can't be renamed, moved or removed, if so.
func (o *oneDrive) fixedOrVirtual(ctx context.Context, p string) error {
	if _, ok, err := o.place(ctx, p); ok {
		if err != nil {
			return err
		}
		return errOneDriveFixed
	}
	return nil
}

func (o *oneDrive) mkdir(ctx context.Context, p string) error {
	if _, ok, err := o.place(ctx, p); ok {
		if errors.Is(err, os.ErrNotExist) {
			return errOneDriveVirtual
		}
		if err != nil {
			return err
		}
		return os.ErrExist
	}
	return o.at(ctx, path.Dir(p), false, func(l graphLoc) error {
		return o.call(ctx, http.MethodPost, l.url("/children"), map[string]any{
			"name": path.Base(p), "folder": map[string]any{}, "@microsoft.graph.conflictBehavior": "fail",
		}, nil)
	})
}

func (o *oneDrive) createEmpty(ctx context.Context, p string) error {
	return o.putSimple(ctx, p, nil, "fail")
}

// remove moves p to the recycle bin of the drive holding it. A link is
// removed from My files; the folder it opens stays.
func (o *oneDrive) remove(ctx context.Context, p string) error {
	if err := o.fixedOrVirtual(ctx, p); err != nil {
		return err
	}
	err := o.at(ctx, p, true, func(l graphLoc) error {
		return o.call(ctx, http.MethodDelete, l.url(""), nil, nil)
	})
	o.forget(p)
	return err
}

func (o *oneDrive) move(ctx context.Context, from, to string) error {
	if err := o.fixedOrVirtual(ctx, from); err != nil {
		return err
	}
	if _, ok, _ := o.place(ctx, to); ok {
		return errOneDriveFixed // can't become a place or a shared item
	}
	src, err := o.item(ctx, from, true)
	if err != nil {
		return err
	}
	parent, err := o.item(ctx, path.Dir(to), false)
	if err != nil {
		if pl, ok, _ := o.place(ctx, path.Dir(to)); ok && pl.virtual {
			return errOneDriveVirtual
		}
		return err
	}
	parentRef := graphRef{driveID: parent.driveID(), id: parent.ID}
	if ref, ok := parent.target(); ok {
		parentRef = ref
	}
	if parentRef.driveID == "" {
		me, err := o.drive(ctx)
		if err != nil {
			return err
		}
		parentRef.driveID = me.ID
	}
	srcDrive := src.driveID()
	if srcDrive != parentRef.driveID {
		return vfs.ErrNotSupported // another drive: copied and deleted instead
	}
	// Replace a file at the destination, as rename(2) does — unless it's
	// the source itself, as in a case-only rename (OneDrive ignores case).
	if dst, err := o.item(ctx, to, true); err == nil && dst.ID != src.ID {
		if dst.isDir() {
			return os.ErrExist
		}
		if err := o.remove(ctx, to); err != nil {
			return err
		}
	}
	err = o.call(ctx, http.MethodPatch, graphAPI+refBase(graphRef{driveID: srcDrive, id: src.ID}), map[string]any{
		"name": path.Base(to), "parentReference": map[string]any{"id": parentRef.id},
	}, nil)
	o.forget(from)
	o.forget(to)
	return err
}

func (o *oneDrive) download(ctx context.Context, p string, off int64) (io.ReadCloser, error) {
	it, err := o.item(ctx, p, false)
	if err != nil {
		return nil, err
	}
	if it.isDir() {
		return nil, errIsDir
	}
	if ref, ok := it.target(); ok {
		// A link to a file: the file itself has the download address.
		if err := o.call(ctx, http.MethodGet, graphAPI+refBase(ref), nil, &it); err != nil {
			return nil, err
		}
	}
	if off >= it.Size || it.Size == 0 {
		return io.NopCloser(strings.NewReader("")), nil
	}
	if it.DownloadURL == "" {
		return nil, errors.New("OneDrive gave no download address for the file")
	}
	resp, err := doRetry(ctx, plainClient, defaultRetry, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodGet, it.DownloadURL, nil)
		if err == nil && off > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
		}
		return req, err
	}, graphParseErr)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// putSimple uploads a small file in one request; conflict is Graph's
// conflict behavior: "replace" or "fail".
func (o *oneDrive) putSimple(ctx context.Context, p string, data []byte, conflict string) error {
	if _, ok, err := o.place(ctx, p); ok {
		if errors.Is(err, os.ErrNotExist) {
			return errOneDriveVirtual
		}
		if err != nil {
			return err
		}
		return errIsDir
	}
	return o.at(ctx, p, false, func(l graphLoc) error {
		u := l.url("/content") + "?@microsoft.graph.conflictBehavior=" + conflict
		resp, err := doRetry(ctx, o.client, defaultRetry, func() (*http.Request, error) {
			req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(data))
			if err == nil {
				req.ContentLength = int64(len(data))
				req.Header.Set("Content-Type", "application/octet-stream")
			}
			return req, err
		}, graphParseErr)
		if err != nil {
			return err
		}
		return decodeJSON(resp, nil)
	})
}

func (o *oneDrive) upload(ctx context.Context, p string, size int64, r io.Reader) error {
	if size < 0 {
		return errors.New("OneDrive needs the size of a file before uploading it")
	}
	if it, err := o.item(ctx, p, false); err == nil && it.isDir() {
		return errIsDir
	}
	if size <= oneDriveSimpleMax {
		data := make([]byte, size)
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		return o.putSimple(ctx, p, data, "replace")
	}

	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	if _, ok, _ := o.place(ctx, p); ok {
		return errOneDriveVirtual
	}
	err := o.at(ctx, p, false, func(l graphLoc) error {
		return o.call(ctx, http.MethodPost, l.url("/createUploadSession"), map[string]any{
			"item": map[string]any{"@microsoft.graph.conflictBehavior": "replace"},
		}, &session)
	})
	if err != nil {
		return err
	}
	if session.UploadURL == "" {
		return errors.New("OneDrive opened no upload session")
	}
	if err := o.sendChunks(ctx, session.UploadURL, size, r); err != nil {
		// Drop the session's partial data (best effort).
		if req, rerr := http.NewRequestWithContext(context.Background(), http.MethodDelete, session.UploadURL, nil); rerr == nil {
			if resp, derr := plainClient.Do(req); derr == nil {
				resp.Body.Close()
			}
		}
		return err
	}
	return nil
}

func (o *oneDrive) sendChunks(ctx context.Context, uploadURL string, size int64, r io.Reader) error {
	buf := make([]byte, oneDriveChunk)
	for start := int64(0); start < size; {
		n := min(int64(len(buf)), size-start)
		chunk := buf[:n]
		if _, err := io.ReadFull(r, chunk); err != nil {
			return err
		}
		rng := fmt.Sprintf("bytes %d-%d/%d", start, start+n-1, size)
		resp, err := doRetry(ctx, plainClient, defaultRetry, func() (*http.Request, error) {
			req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(chunk))
			if err == nil {
				req.ContentLength = n
				req.Header.Set("Content-Range", rng)
			}
			return req, err
		}, graphParseErr)
		if err != nil {
			return err
		}
		if err := decodeJSON(resp, nil); err != nil {
			return err
		}
		start += n
		if start >= size && resp.StatusCode == http.StatusAccepted {
			return errors.New("OneDrive didn't complete the upload")
		}
	}
	return nil
}

func (o *oneDrive) space(ctx context.Context) (total, free uint64, err error) {
	var d struct {
		Quota *struct {
			Total     uint64 `json:"total"`
			Remaining uint64 `json:"remaining"`
		} `json:"quota"`
	}
	if err := o.call(ctx, http.MethodGet, graphAPI+"/me/drive?$select=quota", nil, &d); err != nil {
		return 0, 0, err
	}
	if d.Quota == nil || d.Quota.Total == 0 {
		return 0, 0, vfs.ErrNotSupported
	}
	return d.Quota.Total, d.Quota.Remaining, nil
}

func (o *oneDrive) setModTime(ctx context.Context, p string, t time.Time) error {
	if _, ok, _ := o.place(ctx, p); ok {
		return errOneDriveFixed
	}
	return o.at(ctx, p, false, func(l graphLoc) error {
		return o.call(ctx, http.MethodPatch, l.url(""), map[string]any{
			"fileSystemInfo": map[string]any{"lastModifiedDateTime": t.UTC().Format(time.RFC3339Nano)},
		}, nil)
	})
}

func (o *oneDrive) account(ctx context.Context) (string, error) {
	var me struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
		DisplayName       string `json:"displayName"`
	}
	if err := o.call(ctx, http.MethodGet, graphAPI+"/me?$select=mail,userPrincipalName,displayName", nil, &me); err != nil {
		return "", err
	}
	return firstNonEmpty(me.Mail, me.UserPrincipalName, me.DisplayName, "OneDrive"), nil
}
