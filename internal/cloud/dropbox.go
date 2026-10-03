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
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	dbx "github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/retry"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/users"
	"golang.org/x/oauth2"

	"shfm/internal/vfs"
)

// Dropbox, through its official Go SDK (github.com/dropbox/
// dropbox-sdk-go-unofficial, maintained by Dropbox despite the name),
// which retries throttled requests itself. Dropbox is path-based, and its
// upload sessions take content of unknown size, so it maps directly onto
// the backend interface. It keeps each file's own modification time only
// as given at upload: it can't be changed afterwards (no TimesSetter).

var (
	dropboxEndpoint = oauth2.Endpoint{
		AuthURL:   "https://www.dropbox.com/oauth2/authorize",
		TokenURL:  "https://api.dropboxapi.com/oauth2/token",
		AuthStyle: oauth2.AuthStyleInParams,
	}
	// dropboxURL, when set (by tests), replaces the SDK's API addresses.
	dropboxURL func(hostType, namespace, route string) string
)

const (
	// dropboxRedirectPort is fixed: Dropbox only accepts a redirect URI
	// registered exactly, port included (the one rclone uses too, so an
	// app registered for it works as is).
	dropboxRedirectPort = 53682
)

// dropboxChunk is the size of an upload request, kept in memory so the SDK
// can resend it. A variable so that tests can make uploads of several
// chunks small.
var dropboxChunk = 8 << 20

func init() {
	register(&provider{
		info: ProviderInfo{
			ID: Dropbox, Name: "Dropbox", DefaultClientID: dropboxAppKey,
			RedirectURI: fmt.Sprintf("http://localhost:%d/", dropboxRedirectPort),
		},
		endpoint: func() oauth2.Endpoint { return dropboxEndpoint },
		// The scopes are the app's own (files.metadata.*, files.content.*,
		// account_info.read), set when registering it.
		authParams:   []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("token_access_type", "offline")},
		redirectHost: "localhost",
		redirectPort: dropboxRedirectPort,
		noRedirectOK: true,
		newBackend:   func(c *http.Client, _ oauth2.TokenSource) backend { return newDropbox(c) },
		caps:         caps{streamUpload: true, modTime: false},
	})
}

type dropbox struct {
	files files.ContextClient
	users users.ContextClient
}

func newDropbox(c *http.Client) *dropbox {
	cfg := dbx.Config{
		Client:       c,
		RetryPolicy:  &retry.Policy{MaxRetries: 6, MaxRetryAfter: 60 * time.Second},
		URLGenerator: dropboxURL,
	}
	return &dropbox{files: files.NewContext(cfg), users: users.NewContext(cfg)}
}

// dbxPath is p as Dropbox spells it: its root is "".
func dbxPath(p string) string {
	if p == "/" {
		return ""
	}
	return p
}

// dropboxErr turns the SDK's errors, whose summaries ("path/not_found/..")
// tell what went wrong, into errors the rest of shfm understands.
func dropboxErr(err error) error {
	if err == nil {
		return nil
	}
	var ne net.Error
	var ue *url.Error
	if errors.Is(err, ErrAuthorization) || errors.Is(err, context.Canceled) ||
		errors.As(err, &ne) || errors.As(err, &ue) {
		return err
	}
	summary := err.Error()
	e := &APIError{Message: summary}
	var internal dbx.SDKInternalError
	if errors.As(err, &internal) {
		e.Status, e.Message = internal.StatusCode, internal.Content
	}
	switch {
	case strings.Contains(summary, "invalid_access_token"), strings.Contains(summary, "expired_access_token"):
		e.kind = ErrAuthorization
	case strings.Contains(summary, "not_found"):
		e.kind = os.ErrNotExist
	case strings.Contains(summary, "not_file"):
		return errIsDir
	case strings.Contains(summary, "not_folder"):
		return errNotDir
	case strings.Contains(summary, "conflict"):
		e.kind = os.ErrExist
	case strings.Contains(summary, "no_write_permission"), strings.Contains(summary, "no_permission"),
		strings.Contains(summary, "access_denied"),
		strings.Contains(summary, "restricted_content"):
		e.kind = os.ErrPermission
	case strings.Contains(summary, "insufficient_space"):
		e.Message = "not enough space left on Dropbox"
	}
	return e
}

func dropboxEntry(m files.IsMetadata) (vfs.Entry, bool) {
	switch m := m.(type) {
	case *files.FileMetadata:
		return fileEntry(m.Name, int64(m.Size), time.Time(m.ClientModified)), true
	case *files.FolderMetadata:
		return dirEntry(m.Name, time.Time{}), true
	}
	return vfs.Entry{}, false // deleted
}

func dropboxID(m files.IsMetadata) string {
	switch m := m.(type) {
	case *files.FileMetadata:
		return m.Id
	case *files.FolderMetadata:
		return m.Id
	}
	return ""
}

func (d *dropbox) metadata(ctx context.Context, p string) (files.IsMetadata, error) {
	m, err := d.files.GetMetadataContext(ctx, files.NewGetMetadataArg(p))
	if err != nil {
		return nil, dropboxErr(err)
	}
	if _, ok := dropboxEntry(m); !ok {
		return nil, os.ErrNotExist
	}
	return m, nil
}

func (d *dropbox) stat(ctx context.Context, p string) (vfs.Entry, error) {
	if p == "/" {
		return dirEntry("/", time.Time{}), nil
	}
	m, err := d.metadata(ctx, p)
	if err != nil {
		return vfs.Entry{}, err
	}
	e, _ := dropboxEntry(m)
	return e, nil
}

func (d *dropbox) list(ctx context.Context, p string) ([]vfs.Entry, error) {
	arg := files.NewListFolderArg(dbxPath(p))
	arg.Limit = 2000
	res, err := d.files.ListFolderContext(ctx, arg)
	if err != nil {
		return nil, dropboxErr(err)
	}
	entries := []vfs.Entry{}
	for {
		for _, m := range res.Entries {
			if e, ok := dropboxEntry(m); ok {
				entries = append(entries, e)
			}
		}
		if !res.HasMore {
			return entries, nil
		}
		if res, err = d.files.ListFolderContinueContext(ctx, files.NewListFolderContinueArg(res.Cursor)); err != nil {
			return nil, dropboxErr(err)
		}
	}
}

func (d *dropbox) mkdir(ctx context.Context, p string) error {
	_, err := d.files.CreateFolderV2Context(ctx, files.NewCreateFolderArg(p))
	return dropboxErr(err)
}

func (d *dropbox) createEmpty(ctx context.Context, p string) error {
	arg := files.NewUploadArg(p)
	arg.Mode = &files.WriteMode{Tagged: dbx.Tagged{Tag: files.WriteModeAdd}}
	arg.Mute = true
	_, err := d.files.UploadContext(ctx, arg, bytes.NewReader(nil))
	return dropboxErr(err)
}

// remove deletes p, which Dropbox keeps restorable for a while.
func (d *dropbox) remove(ctx context.Context, p string) error {
	_, err := d.files.DeleteV2Context(ctx, files.NewDeleteArg(p))
	return dropboxErr(err)
}

func (d *dropbox) move(ctx context.Context, from, to string) error {
	src, err := d.metadata(ctx, from)
	if err != nil {
		return err
	}
	// Replace a file at the destination, as rename(2) does — unless it's
	// the source itself, as in a case-only rename (Dropbox ignores case).
	if dst, err := d.metadata(ctx, to); err == nil && dropboxID(dst) != dropboxID(src) {
		if _, isDir := dst.(*files.FolderMetadata); isDir {
			return os.ErrExist
		}
		if err := d.remove(ctx, to); err != nil {
			return err
		}
	}
	_, err = d.files.MoveV2Context(ctx, files.NewRelocationArg(from, to))
	return dropboxErr(err)
}

func (d *dropbox) download(ctx context.Context, p string, off int64) (io.ReadCloser, error) {
	arg := files.NewDownloadArg(p)
	if off > 0 {
		arg.ExtraHeaders = map[string]string{"Range": fmt.Sprintf("bytes=%d-", off)}
	}
	_, r, err := d.files.DownloadContext(ctx, arg)
	if err != nil {
		var internal dbx.SDKInternalError
		if errors.As(err, &internal) && internal.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			return io.NopCloser(strings.NewReader("")), nil // reading at the very end
		}
		return nil, dropboxErr(err)
	}
	return r, nil
}

// upload sends a small file in one request, a larger one (or one of
// unknown size that turns out larger) through an upload session.
func (d *dropbox) upload(ctx context.Context, p string, size int64, r io.Reader) error {
	commit := files.NewCommitInfo(p)
	commit.Mode = &files.WriteMode{Tagged: dbx.Tagged{Tag: files.WriteModeOverwrite}}
	commit.Mute = true

	buf := make([]byte, dropboxChunk)
	n, err := io.ReadFull(r, buf)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		arg := &files.UploadArg{CommitInfo: *commit}
		_, err := d.files.UploadContext(ctx, arg, bytes.NewReader(buf[:n]))
		return dropboxErr(err)
	}
	if err != nil {
		return err
	}

	start, err := d.files.UploadSessionStartContext(ctx, files.NewUploadSessionStartArg(), bytes.NewReader(buf[:n]))
	if err != nil {
		return dropboxErr(err)
	}
	cursor := files.NewUploadSessionCursor(start.SessionId, uint64(n))
	for {
		n, err := io.ReadFull(r, buf)
		last := err == io.EOF || err == io.ErrUnexpectedEOF
		if err != nil && !last {
			return err
		}
		if last {
			_, err := d.files.UploadSessionFinishContext(ctx, files.NewUploadSessionFinishArg(cursor, commit), bytes.NewReader(buf[:n]))
			return dropboxErr(err)
		}
		if err := d.files.UploadSessionAppendV2Context(ctx, files.NewUploadSessionAppendArg(cursor), bytes.NewReader(buf[:n])); err != nil {
			return dropboxErr(err)
		}
		cursor = files.NewUploadSessionCursor(cursor.SessionId, cursor.Offset+uint64(n))
	}
}

func (d *dropbox) space(ctx context.Context) (total, free uint64, err error) {
	u, err := d.users.GetSpaceUsageContext(ctx)
	if err != nil {
		return 0, 0, dropboxErr(err)
	}
	switch {
	case u.Allocation != nil && u.Allocation.Individual != nil:
		total = u.Allocation.Individual.Allocated
	case u.Allocation != nil && u.Allocation.Team != nil:
		total = u.Allocation.Team.Allocated
	}
	if total == 0 {
		return 0, 0, vfs.ErrNotSupported
	}
	if u.Used < total {
		free = total - u.Used
	}
	return total, free, nil
}

func (d *dropbox) setModTime(ctx context.Context, p string, t time.Time) error {
	return vfs.ErrNotSupported
}

func (d *dropbox) account(ctx context.Context) (string, error) {
	a, err := d.users.GetCurrentAccountContext(ctx)
	if err != nil {
		return "", dropboxErr(err)
	}
	return firstNonEmpty(a.Email, a.Name.DisplayName, "Dropbox"), nil
}
