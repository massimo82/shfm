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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"golang.org/x/oauth2"
)

// plainClient makes the requests that carry no OAuth token of ours: token
// exchanges, and the pre-authenticated URLs Microsoft hands out for
// downloads and upload sessions (sending them a token can fail them). No
// overall timeout, since a download can take hours; the connection, TLS
// handshake and response headers each have one instead, so an unreachable
// service fails fast.
var plainClient = &http.Client{Transport: newTransport()}

func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   20 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   8,
		ForceAttemptHTTP2:     true,
	}
}

// bgContext is the context token refreshes run in: it makes the oauth2
// package use plainClient.
func bgContext() context.Context {
	return context.WithValue(context.Background(), oauth2.HTTPClient, plainClient)
}

// authClient returns an HTTP client adding ts's token to every request.
func authClient(ts oauth2.TokenSource) *http.Client {
	return &http.Client{Transport: &oauth2.Transport{Source: ts, Base: plainClient.Transport}}
}

// APIError is an error answer from a service. It's never a lost
// connection (see vfs.IsConnectionFailure): the service answered.
type APIError struct {
	Status  int    // HTTP status
	Code    string // the service's own error code, if any
	Message string
	kind    error // os.ErrNotExist, os.ErrExist, os.ErrPermission, ErrAuthorization or nil
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if e.Code != "" {
		return fmt.Sprintf("%s (%s, HTTP %d)", msg, e.Code, e.Status)
	}
	return fmt.Sprintf("%s (HTTP %d)", msg, e.Status)
}

// Unwrap makes errors.Is(err, os.ErrNotExist) and the like work.
func (e *APIError) Unwrap() error { return e.kind }

// ConnectionFailure implements the classification vfs.IsConnectionFailure
// looks for.
func (e *APIError) ConnectionFailure() bool { return false }

// kindOfStatus maps an HTTP status to the os error it means.
func kindOfStatus(status int) error {
	switch status {
	case http.StatusNotFound, http.StatusGone:
		return os.ErrNotExist
	case http.StatusConflict, http.StatusPreconditionFailed:
		return os.ErrExist
	case http.StatusForbidden:
		return os.ErrPermission
	case http.StatusUnauthorized:
		return ErrAuthorization
	}
	return nil
}

// retryPolicy decides which failed requests are worth retrying.
type retryPolicy struct {
	attempts int           // at most, the first one included
	base     time.Duration // first backoff, doubled at every retry
	max      time.Duration // longest single wait

	// extra, when set, recognizes the service's own retryable answers
	// among the others (Google's 403 rateLimitExceeded); body is the
	// response's, already read.
	extra func(status int, body []byte) bool
}

var defaultRetry = retryPolicy{attempts: 7, base: time.Second, max: 32 * time.Second}

func (p retryPolicy) retryable(status int, body []byte) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return p.extra != nil && p.extra(status, body)
}

// wait returns how long to wait before attempt (1 for the first retry):
// what the service asked for in Retry-After, if anything, or an
// exponential backoff with some jitter, so that parallel transfers
// throttled together don't all come back at the same instant.
func (p retryPolicy) wait(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if s := resp.Header.Get("Retry-After"); s != "" {
			if secs, err := strconv.Atoi(s); err == nil && secs >= 0 {
				return min(time.Duration(secs)*time.Second, 2*p.max)
			}
			if t, err := http.ParseTime(s); err == nil {
				return min(max(time.Until(t), 0), 2*p.max)
			}
		}
	}
	d := p.base << (attempt - 1)
	if d <= 0 || d > p.max {
		d = p.max
	}
	return d/2 + rand.N(d/2+1)
}

// sleepCtx waits d, or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// doRetry sends the request newReq builds — built again for every
// attempt, so its body can be resent — retrying network failures and
// retryable answers. A successful (2xx, or 3xx when the client doesn't
// follow it) response is returned open; any other ends up in parseErr,
// which turns its body into the service's error.
func doRetry(ctx context.Context, client *http.Client, p retryPolicy, newReq func() (*http.Request, error),
	parseErr func(status int, body []byte) error) (*http.Response, error) {
	for attempt := 1; ; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req.WithContext(ctx))
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var authErr error
			if errors.Is(err, ErrAuthorization) {
				authErr = err
			}
			if authErr != nil || attempt >= p.attempts {
				return nil, err
			}
			if err := sleepCtx(ctx, p.wait(attempt, nil)); err != nil {
				return nil, err
			}
			continue
		}
		if resp.StatusCode < 400 {
			return resp, nil
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if attempt < p.attempts && p.retryable(resp.StatusCode, body) {
			if err := sleepCtx(ctx, p.wait(attempt, resp)); err != nil {
				return nil, err
			}
			continue
		}
		return nil, parseErr(resp.StatusCode, body)
	}
}

// decodeJSON reads resp's JSON body into v and closes it.
func decodeJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	if v == nil {
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decoding the service's answer: %w", err)
	}
	return nil
}
