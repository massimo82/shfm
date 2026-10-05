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
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"shfm/internal/vfs"
)

// Google Drive, through its REST API v3 (https://developers.google.com/
// drive/api/reference/rest/v3) — plain HTTP and JSON: Google's own Go
// client library would bring gRPC and OpenTelemetry along.
//
// Drive isn't a filesystem: files are identified by ID, live in folders
// by reference, and a folder may hold several files with the same name.
// Paths are resolved to IDs one folder at a time, folder IDs being cached
// by path; of several files with the same name, the most recently
// modified one is the one a path means. A '/' in a name (allowed by Drive)
// is shown as the look-alike '／' (U+FF0F), as rclone does.
//
// Google's own documents (Docs, Sheets, Slides, Drawings, Apps Script)
// have no file content of their own: they're listed with the extension of
// the OpenDocument (or SVG/JSON) format they're exported to when read,
// read-only and of unknown size (vfs.Entry.SizeUnknown). The other Google
// types (Forms, Sites, My Maps...) can't be exported at all, and aren't
// listed. Shortcuts are listed as links to their target.
//
// The account's root holds Drive's three places, as folders: "My Drive",
// "Shared drives" (one folder per shared drive the account is a member of)
// and "Shared with me" (the files and folders others shared with it). The
// places themselves, the shared drives and the items shared with the
// account can't be created, renamed, moved or removed from shfm — that
// would act on other people's files, or on Drive's own structure — but
// what's inside them can, as far as the account is allowed to.

var (
	driveAPI    = "https://www.googleapis.com/drive/v3"
	driveUpload = "https://www.googleapis.com/upload/drive/v3"
	// driveEndpoint is a variable so that tests can redirect it. Spelled
	// out rather than taken from golang.org/x/oauth2/google, which would
	// bring Google Cloud's metadata client along.
	driveEndpoint = oauth2.Endpoint{
		AuthURL:   "https://accounts.google.com/o/oauth2/auth",
		TokenURL:  "https://oauth2.googleapis.com/token",
		AuthStyle: oauth2.AuthStyleInParams,
	}
)

const (
	driveFolderMime   = "application/vnd.google-apps.folder"
	driveShortcutMime = "application/vnd.google-apps.shortcut"
	driveGoogleType   = "application/vnd.google-apps."
	driveFields       = "id,name,mimeType,size,modifiedTime,shortcutDetails,driveId"
)

// Drive's places, the folders at the account's root.
const (
	driveMyDrive      = "My Drive"
	driveSharedDrives = "Shared drives"
	driveSharedWithMe = "Shared with me"
)

// driveChunk is the size of an upload request: a multiple of 256 KiB, as
// resumable uploads require, kept in memory so it can be resent. A
// variable so that tests can make uploads of several chunks small.
var driveChunk = 8 << 20

// driveExports maps the exportable Google types to the format they're
// read as.
var driveExports = map[string]struct{ ext, mime string }{
	"application/vnd.google-apps.document":     {".odt", "application/vnd.oasis.opendocument.text"},
	"application/vnd.google-apps.spreadsheet":  {".ods", "application/x-vnd.oasis.opendocument.spreadsheet"},
	"application/vnd.google-apps.presentation": {".odp", "application/vnd.oasis.opendocument.presentation"},
	"application/vnd.google-apps.drawing":      {".svg", "image/svg+xml"},
	"application/vnd.google-apps.script":       {".json", "application/vnd.google-apps.script+json"},
}

func init() {
	register(&provider{
		info: ProviderInfo{
			ID: GoogleDrive, Name: "Google Drive", DefaultClientID: googleClientID,
			NeedsSecret: true, RedirectURI: "http://127.0.0.1 (any port)",
		},
		endpoint: func() oauth2.Endpoint { return driveEndpoint },
		scopes:   []string{"https://www.googleapis.com/auth/drive"},
		// A refresh token is only handed out with offline access, and
		// again for an account already authorized only with consent.
		authParams:    []oauth2.AuthCodeOption{oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent")},
		redirectHost:  "127.0.0.1",
		newBackend:    func(c *http.Client, _ oauth2.TokenSource) backend { return newDrive(c) },
		caps:          caps{streamUpload: true, modTime: true, home: "/" + driveMyDrive},
		defaultSecret: func() string { return googleClientSecret },
	})
}

type drive struct {
	client *http.Client

	mu       sync.Mutex
	dirs     map[string]driveDir // by folder path
	drives   []driveFile         // the shared drives (ID, name), as of drivesAt
	drivesAt time.Time
}

// driveDir is a cached folder ID (a shortcut's: its target's), trusted for
// driveDirTTL: changes made elsewhere (a folder renamed in the browser)
// show within that time, while browsing doesn't resolve every folder of a
// path again at every step.
type driveDir struct {
	id      string
	driveID string // the shared drive holding it, "" elsewhere
	at      time.Time
}

var driveDirTTL = 30 * time.Second

func newDrive(c *http.Client) *drive {
	return &drive{client: c, dirs: map[string]driveDir{}}
}

type driveFile struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	MimeType        string    `json:"mimeType"`
	Size            int64     `json:"size,string"`
	ModifiedTime    time.Time `json:"modifiedTime"`
	DriveID         string    `json:"driveId"`
	ShortcutDetails *struct {
		TargetID       string `json:"targetId"`
		TargetMimeType string `json:"targetMimeType"`
	} `json:"shortcutDetails,omitempty"`
}

// driveNode is a resolved path.
type driveNode struct {
	id      string // what to read or list: a shortcut's target
	selfID  string // what to rename, move or remove: the shortcut itself
	parent  string // parent folder ID
	driveID string // the shared drive holding it, "" elsewhere
	entry   vfs.Entry
	export  string // the export MIME type, for a Google document

	// fixed: a place, a shared drive or an item shared with the account,
	// which can't be renamed, moved or removed. virtual: no Drive folder
	// behind it (the root, Shared drives, Shared with me), so nothing can
	// be created in it either.
	fixed, virtual bool
}

var (
	errDriveFixed = &APIError{Status: http.StatusForbidden, kind: os.ErrPermission,
		Message: "Drive's places can't be renamed, moved or removed"}
	errDriveVirtual = &APIError{Status: http.StatusForbidden, kind: os.ErrPermission,
		Message: "nothing can be created here, only inside folders"}
)

// --- names ----------------------------------------------------------------

func driveDecodeName(n string) string { return strings.ReplaceAll(n, "／", "/") }
func driveEncodeName(n string) string { return strings.ReplaceAll(n, "/", "／") }

func driveQuote(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// driveEntry turns a file into its entry; ok is false for a file not
// listed (a Google type that can't be exported).
func driveEntry(f driveFile) (e vfs.Entry, export string, ok bool) {
	name := driveEncodeName(f.Name)
	mime := f.MimeType
	link := false
	if mime == driveShortcutMime && f.ShortcutDetails != nil {
		mime, link = f.ShortcutDetails.TargetMimeType, true
	}
	switch {
	case mime == driveFolderMime:
		e = dirEntry(name, f.ModifiedTime)
	case strings.HasPrefix(mime, driveGoogleType):
		x, exportable := driveExports[mime]
		if !exportable {
			return vfs.Entry{}, "", false
		}
		e = fileEntry(name+x.ext, 0, f.ModifiedTime)
		e.Mode, e.SizeUnknown, export = 0o444, true, x.mime
	default:
		e = fileEntry(name, f.Size, f.ModifiedTime)
		if link {
			e.SizeUnknown = true // the shortcut's own size, not the target's
		}
	}
	e.IsSymlink = link
	return e, export, true
}

func (d *drive) nodeOf(f driveFile, parent string) (driveNode, bool) {
	e, export, ok := driveEntry(f)
	if !ok {
		return driveNode{}, false
	}
	n := driveNode{id: f.ID, selfID: f.ID, parent: parent, driveID: f.DriveID, entry: e, export: export}
	if f.ShortcutDetails != nil {
		n.id = f.ShortcutDetails.TargetID
		n.driveID = "" // the target's, found when it's a folder entered (see resolve)
	}
	return n, true
}

// --- requests ---------------------------------------------------------------

var drivePolicy = retryPolicy{
	attempts: defaultRetry.attempts, base: defaultRetry.base, max: defaultRetry.max,
	extra: func(status int, body []byte) bool {
		if status != http.StatusForbidden {
			return false
		}
		reason := driveErrReason(body)
		return reason == "rateLimitExceeded" || reason == "userRateLimitExceeded"
	},
}

type driveErrBody struct {
	Error struct {
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
	} `json:"error"`
}

func driveErrReason(body []byte) string {
	var b driveErrBody
	if json.Unmarshal(body, &b) == nil && len(b.Error.Errors) > 0 {
		return b.Error.Errors[0].Reason
	}
	return ""
}

func driveParseErr(status int, body []byte) error {
	var b driveErrBody
	_ = json.Unmarshal(body, &b)
	e := &APIError{Status: status, Message: b.Error.Message, kind: kindOfStatus(status)}
	if len(b.Error.Errors) > 0 {
		e.Code = b.Error.Errors[0].Reason
	}
	if status == http.StatusForbidden && strings.Contains(e.Code, "RateLimit") {
		e.kind = nil // throttled, not denied
	}
	return e
}

// call sends a JSON request (body: nil or a value to encode) and decodes
// the answer into out (nil to discard it).
func (d *drive) call(ctx context.Context, method, u string, body, out any) error {
	var data []byte
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return err
		}
	}
	resp, err := doRetry(ctx, d.client, drivePolicy, func() (*http.Request, error) {
		var r io.Reader
		if data != nil {
			r = bytes.NewReader(data)
		}
		req, err := http.NewRequest(method, u, r)
		if err == nil && data != nil {
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
		}
		return req, err
	}, driveParseErr)
	if err != nil {
		return err
	}
	return decodeJSON(resp, out)
}

func driveURL(base, p string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	q.Set("supportsAllDrives", "true")
	return base + p + "?" + q.Encode()
}

// query lists the files matching q, all pages: in shared drive driveID,
// or else in My Drive and what's shared with the account.
func (d *drive) query(ctx context.Context, q, driveID string, limit int) ([]driveFile, error) {
	var out []driveFile
	token := ""
	for {
		v := url.Values{
			"q":                         {q},
			"fields":                    {"nextPageToken,files(" + driveFields + ")"},
			"pageSize":                  {"1000"},
			"orderBy":                   {"folder,modifiedTime desc"},
			"includeItemsFromAllDrives": {"true"},
		}
		if limit > 0 {
			v.Set("pageSize", strconv.Itoa(limit))
		}
		if driveID != "" {
			v.Set("corpora", "drive")
			v.Set("driveId", driveID)
		}
		if token != "" {
			v.Set("pageToken", token)
		}
		var page struct {
			NextPageToken string      `json:"nextPageToken"`
			Files         []driveFile `json:"files"`
		}
		if err := d.call(ctx, http.MethodGet, driveURL(driveAPI, "/files", v), nil, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Files...)
		token = page.NextPageToken
		if token == "" || limit > 0 {
			return out, nil
		}
	}
}

// --- path resolution --------------------------------------------------------

func (d *drive) cachedDir(p string) (driveDir, bool) {
	if p == "/"+driveMyDrive {
		return driveDir{id: "root"}, true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	dir, ok := d.dirs[p]
	if !ok || time.Since(dir.at) >= driveDirTTL {
		return driveDir{}, false
	}
	return dir, true
}

func (d *drive) cacheDir(p, id, driveID string) {
	d.mu.Lock()
	d.dirs[p] = driveDir{id: id, driveID: driveID, at: time.Now()}
	d.mu.Unlock()
}

// forget drops p, and everything under it, from the folder cache.
func (d *drive) forget(p string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k := range d.dirs {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(d.dirs, k)
		}
	}
}

// dir returns folder p: a real Drive folder, so not a virtual one.
func (d *drive) dir(ctx context.Context, p string) (driveDir, error) {
	if dir, ok := d.cachedDir(p); ok {
		return dir, nil
	}
	n, err := d.resolve(ctx, p)
	switch {
	case err != nil:
		return driveDir{}, err
	case n.virtual:
		return driveDir{}, errDriveVirtual
	case !n.entry.IsDir:
		return driveDir{}, errNotDir
	}
	return driveDir{id: n.id, driveID: n.driveID}, nil
}

// child finds name in folder parent; ok is false if there's none.
func (d *drive) child(ctx context.Context, parent driveDir, name string) (driveNode, bool, error) {
	return d.find(ctx, name, func(n string) string {
		return fmt.Sprintf("name = %s and %s in parents and trashed = false", driveQuote(n), driveQuote(parent.id))
	}, parent)
}

// find looks for the file the entry name stands for, with the query q
// makes for a Drive name.
func (d *drive) find(ctx context.Context, name string, q func(name string) string, parent driveDir) (driveNode, bool, error) {
	real := driveDecodeName(name)
	candidates := []string{real}
	// "Report.odt" may be the Google document "Report", exported.
	if ext := path.Ext(real); ext != "" {
		candidates = append(candidates, strings.TrimSuffix(real, ext))
	}
	for _, c := range candidates {
		files, err := d.query(ctx, q(c), parent.driveID, 0)
		if err != nil {
			return driveNode{}, false, err
		}
		for _, f := range files {
			if n, ok := d.nodeOf(f, parent.id); ok && n.entry.Name == name {
				if n.driveID == "" {
					n.driveID = parent.driveID
				}
				return n, true, nil
			}
		}
	}
	return driveNode{}, false, nil
}

// sharedDrives lists the shared drives the account is a member of.
func (d *drive) sharedDrives(ctx context.Context) ([]driveFile, error) {
	d.mu.Lock()
	if d.drives != nil && time.Since(d.drivesAt) < driveDirTTL {
		drives := d.drives
		d.mu.Unlock()
		return drives, nil
	}
	d.mu.Unlock()
	drives := []driveFile{}
	token := ""
	for {
		v := url.Values{"pageSize": {"100"}, "fields": {"nextPageToken,drives(id,name)"}}
		if token != "" {
			v.Set("pageToken", token)
		}
		var page struct {
			NextPageToken string      `json:"nextPageToken"`
			Drives        []driveFile `json:"drives"`
		}
		if err := d.call(ctx, http.MethodGet, driveAPI+"/drives?"+v.Encode(), nil, &page); err != nil {
			return nil, err
		}
		drives = append(drives, page.Drives...)
		if token = page.NextPageToken; token == "" {
			break
		}
	}
	d.mu.Lock()
	d.drives, d.drivesAt = drives, time.Now()
	d.mu.Unlock()
	return drives, nil
}

// place resolves the paths that aren't a file in a Drive folder: the
// root, the three places, the shared drives and the items shared with the
// account. ok is false for any other path.
func (d *drive) place(ctx context.Context, p string) (n driveNode, ok bool, err error) {
	if p == "/" {
		return driveNode{entry: dirEntry("/", time.Time{}), fixed: true, virtual: true}, true, nil
	}
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	switch {
	case len(segs) == 1 && segs[0] == driveMyDrive:
		return driveNode{id: "root", selfID: "root", entry: dirEntry(driveMyDrive, time.Time{}), fixed: true}, true, nil
	case len(segs) == 1 && (segs[0] == driveSharedDrives || segs[0] == driveSharedWithMe):
		return driveNode{entry: dirEntry(segs[0], time.Time{}), fixed: true, virtual: true}, true, nil
	case segs[0] != driveMyDrive && segs[0] != driveSharedDrives && segs[0] != driveSharedWithMe:
		return driveNode{}, true, os.ErrNotExist
	case len(segs) == 2 && segs[0] == driveSharedDrives:
		drives, err := d.sharedDrives(ctx)
		if err != nil {
			return driveNode{}, true, err
		}
		for _, sd := range drives {
			if driveEncodeName(sd.Name) == segs[1] {
				d.cacheDir(p, sd.ID, sd.ID)
				return driveNode{id: sd.ID, selfID: sd.ID, driveID: sd.ID, entry: dirEntry(segs[1], time.Time{}), fixed: true}, true, nil
			}
		}
		return driveNode{}, true, os.ErrNotExist
	case len(segs) == 2 && segs[0] == driveSharedWithMe:
		n, found, err := d.find(ctx, segs[1], func(name string) string {
			return fmt.Sprintf("name = %s and sharedWithMe = true and trashed = false", driveQuote(name))
		}, driveDir{})
		if err != nil {
			return driveNode{}, true, err
		}
		if !found {
			return driveNode{}, true, os.ErrNotExist
		}
		n.parent, n.fixed = "", true
		if n.entry.IsDir {
			d.cacheDir(p, n.id, n.driveID)
		}
		return n, true, nil
	}
	return driveNode{}, false, nil
}

// resolve finds what path p is.
func (d *drive) resolve(ctx context.Context, p string) (driveNode, error) {
	if n, ok, err := d.place(ctx, p); ok {
		return n, err
	}
	parentPath := path.Dir(p)
	for attempt := 0; ; attempt++ {
		_, cached := d.cachedDir(parentPath)
		parent, err := d.dir(ctx, parentPath)
		if err != nil {
			return driveNode{}, err
		}
		n, ok, err := d.child(ctx, parent, path.Base(p))
		if errors.Is(err, os.ErrNotExist) && cached && attempt == 0 {
			// The cached folder is gone (moved or deleted elsewhere).
			d.forget(parentPath)
			continue
		}
		if err != nil {
			return driveNode{}, err
		}
		if !ok {
			// Not retried with the folder looked up afresh: applications
			// probe for many missing files through the FUSE mount, and a
			// folder replaced elsewhere shows once its ID expires.
			return driveNode{}, os.ErrNotExist
		}
		if n.entry.IsDir {
			if n.entry.IsSymlink {
				// A shortcut to a folder, maybe in a shared drive.
				var target driveFile
				if err := d.call(ctx, http.MethodGet, driveURL(driveAPI, "/files/"+url.PathEscape(n.id),
					url.Values{"fields": {"driveId"}}), nil, &target); err == nil {
					n.driveID = target.DriveID
				}
			}
			d.cacheDir(p, n.id, n.driveID)
		}
		return n, nil
	}
}

// --- backend ----------------------------------------------------------------

func (d *drive) stat(ctx context.Context, p string) (vfs.Entry, error) {
	n, err := d.resolve(ctx, p)
	return n.entry, err
}

func (d *drive) list(ctx context.Context, p string) ([]vfs.Entry, error) {
	switch p {
	case "/":
		return []vfs.Entry{
			dirEntry(driveMyDrive, time.Time{}), dirEntry(driveSharedDrives, time.Time{}), dirEntry(driveSharedWithMe, time.Time{}),
		}, nil
	case "/" + driveSharedDrives:
		drives, err := d.sharedDrives(ctx)
		if err != nil {
			return nil, err
		}
		entries := make([]vfs.Entry, 0, len(drives))
		for _, sd := range drives {
			name := driveEncodeName(sd.Name)
			d.cacheDir(path.Join(p, name), sd.ID, sd.ID)
			entries = append(entries, dirEntry(name, time.Time{}))
		}
		return entries, nil
	case "/" + driveSharedWithMe:
		files, err := d.query(ctx, "sharedWithMe = true and trashed = false", "", 0)
		if err != nil {
			return nil, err
		}
		return d.entries(p, files, driveDir{}), nil
	}
	if _, ok, err := d.place(ctx, p); ok && err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		_, cached := d.cachedDir(p)
		dir, err := d.dir(ctx, p)
		if err != nil {
			return nil, err
		}
		files, err := d.query(ctx, fmt.Sprintf("%s in parents and trashed = false", driveQuote(dir.id)), dir.driveID, 0)
		if errors.Is(err, os.ErrNotExist) && cached && attempt == 0 {
			d.forget(p)
			continue
		}
		if err != nil {
			return nil, err
		}
		return d.entries(p, files, dir), nil
	}
}

// entries turns folder p's files into entries, caching its subfolders.
func (d *drive) entries(p string, files []driveFile, dir driveDir) []vfs.Entry {
	entries := make([]vfs.Entry, 0, len(files))
	for _, f := range files {
		n, ok := d.nodeOf(f, dir.id)
		if !ok {
			continue
		}
		if n.entry.IsDir && !n.entry.IsSymlink {
			if n.driveID == "" {
				n.driveID = dir.driveID
			}
			d.cacheDir(path.Join(p, n.entry.Name), n.id, n.driveID)
		}
		entries = append(entries, n.entry)
	}
	return entries
}

// create makes a new file or folder named after p's base in p's parent.
func (d *drive) create(ctx context.Context, p, mime string) (driveFile, error) {
	if _, err := d.resolve(ctx, p); err == nil {
		return driveFile{}, os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return driveFile{}, err
	}
	parent, err := d.dir(ctx, path.Dir(p))
	if err != nil {
		return driveFile{}, err
	}
	meta := map[string]any{"name": driveDecodeName(path.Base(p)), "parents": []string{parent.id}}
	if mime != "" {
		meta["mimeType"] = mime
	}
	var f driveFile
	err = d.call(ctx, http.MethodPost, driveURL(driveAPI, "/files", url.Values{"fields": {driveFields}}), meta, &f)
	return f, err
}

func (d *drive) mkdir(ctx context.Context, p string) error {
	f, err := d.create(ctx, p, driveFolderMime)
	if err == nil {
		d.cacheDir(p, f.ID, f.DriveID)
	}
	return err
}

func (d *drive) createEmpty(ctx context.Context, p string) error {
	_, err := d.create(ctx, p, "")
	return err
}

// remove moves p to Drive's trash, where it can be restored from for 30
// days: Drive's own delete is irreversible.
func (d *drive) remove(ctx context.Context, p string) error {
	n, err := d.resolve(ctx, p)
	if err != nil {
		return err
	}
	if n.fixed {
		return errDriveFixed
	}
	err = d.call(ctx, http.MethodPatch, driveURL(driveAPI, "/files/"+url.PathEscape(n.selfID), nil),
		map[string]any{"trashed": true}, nil)
	d.forget(p)
	return err
}

func (d *drive) move(ctx context.Context, from, to string) error {
	n, err := d.resolve(ctx, from)
	if err != nil {
		return err
	}
	if n.fixed {
		return errDriveFixed
	}
	if _, ok, _ := d.place(ctx, to); ok {
		return errDriveFixed // can't become a place, a shared drive or a shared item
	}
	parent, err := d.dir(ctx, path.Dir(to))
	if err != nil {
		return err
	}
	newParent := parent.id
	// Drive would happily keep two files of the same name: replace the
	// one at the destination, as rename(2) does.
	if old, err := d.resolve(ctx, to); err == nil && old.selfID != n.selfID {
		if old.entry.IsDir {
			return os.ErrExist
		}
		if err := d.remove(ctx, to); err != nil {
			return err
		}
	}
	name := driveDecodeName(path.Base(to))
	if n.export != "" {
		// The extension of the export format isn't part of the name.
		name = strings.TrimSuffix(name, path.Ext(n.entry.Name))
	}
	q := url.Values{"fields": {driveFields}}
	if newParent != n.parent {
		q.Set("addParents", newParent)
		q.Set("removeParents", n.parent)
	}
	err = d.call(ctx, http.MethodPatch, driveURL(driveAPI, "/files/"+url.PathEscape(n.selfID), q),
		map[string]any{"name": name}, nil)
	d.forget(from)
	d.forget(to)
	return err
}

func (d *drive) download(ctx context.Context, p string, off int64) (io.ReadCloser, error) {
	n, err := d.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	if n.entry.IsDir {
		return nil, errIsDir
	}
	u := driveURL(driveAPI, "/files/"+url.PathEscape(n.id), url.Values{"alt": {"media"}})
	if n.export != "" {
		// Exports can't be read from an offset: skip to it.
		u = driveURL(driveAPI, "/files/"+url.PathEscape(n.id)+"/export", url.Values{"mimeType": {n.export}})
	}
	resp, err := doRetry(ctx, d.client, drivePolicy, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err == nil && off > 0 && n.export == "" {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
		}
		return req, err
	}, driveParseErr)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusRequestedRangeNotSatisfiable {
			return io.NopCloser(strings.NewReader("")), nil // reading at the very end
		}
		return nil, err
	}
	if n.export != "" && off > 0 {
		if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
			resp.Body.Close()
			if err == io.EOF {
				return io.NopCloser(strings.NewReader("")), nil
			}
			return nil, err
		}
	}
	return resp.Body, nil
}

// upload sends the content through a resumable upload session, a chunk at
// a time, so that a failed request resends only its chunk.
func (d *drive) upload(ctx context.Context, p string, size int64, r io.Reader) error {
	var sessionReq func() (*http.Request, error)
	n, err := d.resolve(ctx, p)
	switch {
	case err == nil && n.entry.IsDir:
		return errIsDir
	case err == nil && (n.export != "" || n.entry.IsSymlink):
		return errReadOnly
	case err == nil:
		u := driveURL(driveUpload, "/files/"+url.PathEscape(n.selfID), url.Values{"uploadType": {"resumable"}})
		sessionReq = func() (*http.Request, error) { return driveSessionRequest(http.MethodPatch, u, map[string]any{}, size) }
	case errors.Is(err, os.ErrNotExist):
		parent, err := d.dir(ctx, path.Dir(p))
		if err != nil {
			return err
		}
		meta := map[string]any{"name": driveDecodeName(path.Base(p)), "parents": []string{parent.id}}
		u := driveURL(driveUpload, "/files", url.Values{"uploadType": {"resumable"}})
		sessionReq = func() (*http.Request, error) { return driveSessionRequest(http.MethodPost, u, meta, size) }
	default:
		return err
	}

	resp, err := doRetry(ctx, d.client, drivePolicy, sessionReq, driveParseErr)
	if err != nil {
		return err
	}
	resp.Body.Close()
	session := resp.Header.Get("Location")
	if session == "" {
		return errors.New("Google Drive opened no upload session")
	}
	return d.sendChunks(ctx, session, size, r)
}

func driveSessionRequest(method, u string, meta map[string]any, size int64) (*http.Request, error) {
	data, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, u, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if size >= 0 {
		req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	}
	return req, nil
}

// sendChunks uploads r to session. Every chunk but the last is a full
// driveChunk; the total, when size is unknown, is declared with the last.
func (d *drive) sendChunks(ctx context.Context, session string, size int64, r io.Reader) error {
	buf := make([]byte, driveChunk)
	var start int64
	for {
		n, err := io.ReadFull(r, buf)
		last := err == io.EOF || err == io.ErrUnexpectedEOF
		if err != nil && !last {
			return err
		}
		if size >= 0 && start+int64(n) >= size {
			last = true
		}
		total := "*"
		if last {
			total = strconv.FormatInt(start+int64(n), 10)
		} else if size >= 0 {
			total = strconv.FormatInt(size, 10)
		}
		chunk := buf[:n]
		for {
			rng := "bytes */" + total // nothing left to send: just complete it
			if len(chunk) > 0 {
				rng = fmt.Sprintf("bytes %d-%d/%s", start, start+int64(len(chunk))-1, total)
			}
			body := chunk
			resp, err := doRetry(ctx, d.client, drivePolicy, func() (*http.Request, error) {
				req, err := http.NewRequest(http.MethodPut, session, bytes.NewReader(body))
				if err == nil {
					req.ContentLength = int64(len(body))
					req.Header.Set("Content-Range", rng)
				}
				return req, err
			}, driveParseErr)
			if err != nil {
				return err
			}
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK, http.StatusCreated:
				return nil
			case http.StatusPermanentRedirect: // 308 Resume Incomplete
			default:
				return fmt.Errorf("unexpected answer to an upload: %s", resp.Status)
			}
			// Range ("bytes=0-N") says how much has arrived so far.
			got := int64(0)
			if rh := resp.Header.Get("Range"); rh != "" {
				if i := strings.LastIndex(rh, "-"); i >= 0 {
					if end, err := strconv.ParseInt(rh[i+1:], 10, 64); err == nil {
						got = end + 1
					}
				}
			}
			acked := got - start
			if acked < 0 || acked > int64(len(chunk)) || (acked == 0 && len(chunk) > 0) {
				return fmt.Errorf("unexpected upload range from Google Drive (%q)", resp.Header.Get("Range"))
			}
			chunk, start = chunk[acked:], got
			if len(chunk) == 0 {
				if last {
					return errors.New("Google Drive didn't complete the upload")
				}
				break
			}
		}
	}
}

func (d *drive) space(ctx context.Context) (total, free uint64, err error) {
	var about struct {
		StorageQuota struct {
			Limit string `json:"limit"`
			Usage string `json:"usage"`
		} `json:"storageQuota"`
	}
	if err := d.call(ctx, http.MethodGet, driveURL(driveAPI, "/about", url.Values{"fields": {"storageQuota"}}), nil, &about); err != nil {
		return 0, 0, err
	}
	limit, err1 := strconv.ParseUint(about.StorageQuota.Limit, 10, 64)
	usage, err2 := strconv.ParseUint(about.StorageQuota.Usage, 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 0, vfs.ErrNotSupported // an unlimited account
	}
	if usage > limit {
		usage = limit
	}
	return limit, limit - usage, nil
}

func (d *drive) setModTime(ctx context.Context, p string, t time.Time) error {
	n, err := d.resolve(ctx, p)
	if err != nil {
		return err
	}
	if n.virtual || n.selfID == "root" {
		return errDriveFixed
	}
	return d.call(ctx, http.MethodPatch, driveURL(driveAPI, "/files/"+url.PathEscape(n.selfID), nil),
		map[string]any{"modifiedTime": t.UTC().Format(time.RFC3339Nano)}, nil)
}

func (d *drive) account(ctx context.Context) (string, error) {
	var about struct {
		User struct {
			EmailAddress string `json:"emailAddress"`
			DisplayName  string `json:"displayName"`
		} `json:"user"`
	}
	if err := d.call(ctx, http.MethodGet, driveURL(driveAPI, "/about", url.Values{"fields": {"user"}}), nil, &about); err != nil {
		return "", err
	}
	return firstNonEmpty(about.User.EmailAddress, about.User.DisplayName, "Google Drive"), nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
