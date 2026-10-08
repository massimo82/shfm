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

package lsv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"shfm/internal/localsend/protocol"
)

// Client is the sending side of version 2.
type Client struct {
	// HTTP returns the http.Client to reach peer with: its TLS
	// configuration pins the peer's certificate and presents the
	// device's own (see internal/localsend).
	HTTP func(peer protocol.Peer) *http.Client
}

var _ protocol.Client = (*Client)(nil)

// maxAnswer bounds the JSON answers read.
const maxAnswer = 1 << 20

func (c *Client) post(ctx context.Context, peer protocol.Peer, path string, query url.Values, body io.Reader, size int64, contentType string) (*http.Response, error) {
	u := peer.URL() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.HTTP(peer).Do(req)
}

func (c *Client) postJSON(ctx context.Context, peer protocol.Peer, path string, query url.Values, v any) (*http.Response, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return c.post(ctx, peer, path, query, bytes.NewReader(data), int64(len(data)), "application/json")
}

// Register introduces self to peer.
func (c *Client) Register(ctx context.Context, peer protocol.Peer, self protocol.Info) (protocol.Info, error) {
	resp, err := c.postJSON(ctx, peer, pathRegister, nil, toRegister(self))
	if err != nil {
		return protocol.Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return protocol.Info{}, statusError(resp)
	}
	var d infoDTO
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(&d); err != nil {
		return protocol.Info{}, err
	}
	return protocol.Info{
		Alias: d.Alias, Version: d.Version, DeviceModel: d.DeviceModel, DeviceType: normalizeType(d.DeviceType),
		Fingerprint: d.Fingerprint, Port: peer.Port, HTTPS: peer.HTTPS, Download: d.Download,
	}, nil
}

// PrepareUpload offers files to peer.
func (c *Client) PrepareUpload(ctx context.Context, peer protocol.Peer, self protocol.Info, files []protocol.File, pin string) (protocol.Session, error) {
	req := prepareUploadRequest{Info: toRegister(self), Files: make(map[string]fileDTO, len(files))}
	for _, f := range files {
		req.Files[f.ID] = toFileDTO(f)
	}
	var q url.Values
	if pin != "" {
		q = url.Values{"pin": {pin}}
	}
	resp, err := c.postJSON(ctx, peer, pathPrepareUpload, q, req)
	if err != nil {
		return protocol.Session{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNoContent:
		return protocol.Session{}, protocol.ErrNothing
	case http.StatusUnauthorized:
		if pin == "" {
			return protocol.Session{}, protocol.ErrPINRequired
		}
		return protocol.Session{}, protocol.ErrWrongPIN
	case http.StatusForbidden:
		return protocol.Session{}, protocol.ErrDeclined
	case http.StatusConflict:
		return protocol.Session{}, protocol.ErrBusy
	case http.StatusTooManyRequests:
		return protocol.Session{}, protocol.ErrTooManyTries
	default:
		return protocol.Session{}, statusError(resp)
	}
	var d prepareUploadResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(&d); err != nil {
		return protocol.Session{}, err
	}
	if d.SessionID == "" {
		return protocol.Session{}, fmt.Errorf("no session in the answer")
	}
	return protocol.Session{ID: d.SessionID, Tokens: d.Files}, nil
}

// Upload sends one accepted file's content.
func (c *Client) Upload(ctx context.Context, peer protocol.Peer, s protocol.Session, fileID string, body io.Reader, size int64) error {
	q := url.Values{"sessionId": {s.ID}, "fileId": {fileID}, "token": {s.Tokens[fileID]}}
	resp, err := c.post(ctx, peer, pathUpload, q, body, size, "application/octet-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusForbidden:
		return protocol.ErrForbidden
	case http.StatusConflict:
		return protocol.ErrBusy
	case http.StatusUnprocessableEntity:
		return protocol.ErrChecksum
	}
	return statusError(resp)
}

// Cancel tells peer the session is over.
func (c *Client) Cancel(ctx context.Context, peer protocol.Peer, sessionID string) error {
	var q url.Values
	if sessionID != "" {
		q = url.Values{"sessionId": {sessionID}}
	}
	resp, err := c.post(ctx, peer, pathCancel, q, nil, 0, "")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return statusError(resp)
	}
	return nil
}

// statusError reads the message of an error answer: {"message": "..."},
// or plain text.
func statusError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var m struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &m) == nil && m.Message != "" {
		msg = m.Message
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return &protocol.StatusError{Code: resp.StatusCode, Message: msg}
}
