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

// Package protocol holds what every version of the LocalSend protocol
// (https://github.com/localsend/protocol) has in common, as seen by
// internal/localsend: the devices, the files offered, and the two
// interfaces a version implements — Client, the sending side, and
// Server, the receiving side, which asks a Handler (the application)
// what to do. Each version lives in its own package (lsv2 for 2.x), so
// that a new one (3.x, with its nonce and token exchange) is added next
// to it without touching the application.
package protocol

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Defaults of every version so far.
const (
	// MulticastGroup and DefaultPort: the announcements' UDP group, and the
	// port both they and the HTTP server use unless configured otherwise.
	MulticastGroup = "224.0.0.167"
	DefaultPort    = 53317
	// MulticastGroup6 is the IPv6 group (link-local scope) the reference
	// implementation announces on too, alongside IPv4's: an extension of
	// the protocol, which only defines IPv4's.
	MulticastGroup6 = "ff12::fd3a:e420"
)

// Device types (deviceType): only used for icons, an unknown one is
// shown as a desktop.
const (
	TypeMobile   = "mobile"
	TypeDesktop  = "desktop"
	TypeWeb      = "web"
	TypeHeadless = "headless"
	TypeServer   = "server"
)

// Info describes a device as it announces itself.
type Info struct {
	Alias       string
	Version     string // the protocol version it speaks, "major.minor"
	DeviceModel string // may be empty
	DeviceType  string // may be empty
	// Fingerprint identifies the device: over HTTPS, the SHA-256 of its
	// certificate (see CertFingerprint); over HTTP, a random string.
	Fingerprint string
	Port        int
	HTTPS       bool
	// Download: the device serves its files to browsers (the download
	// API), which shfm doesn't use.
	Download bool
}

// Peer is a device and the address it was reached at.
type Peer struct {
	Info
	// IP is an IPv4 or IPv6 address; a link-local IPv6 one carries its
	// zone, the interface it's reached through ("fe80::1%wlan0").
	IP string
}

// URL returns the base address of the peer's server, "https://ip:port"
// ("https://[fe80::1%25wlan0]:port": a zone is escaped in a URL).
func (p Peer) URL() string {
	scheme := "http"
	if p.HTTPS {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(strings.Replace(p.IP, "%", "%25", 1), strconv.Itoa(p.Port))
}

// File is a file offered in a transfer.
type File struct {
	ID string
	// Name is the file's name, or for a file inside a folder sent whole
	// its path from that folder, "/"-separated ("Photos/2024/a.jpg"). It
	// comes from the sender: never trust it as a path.
	Name   string
	Size   int64
	MIME   string
	SHA256 string // lowercase hex, "" when not given
	// Preview, when set on the only file of a text/* transfer, is a text
	// message: the whole of it, with nothing left to download.
	Preview  *string
	Modified time.Time // zero when not given
	Accessed time.Time
}

// Message returns the text message a transfer carries, if it is one: a
// single text file with its content in the preview.
func Message(files []File) (string, bool) {
	if len(files) != 1 || files[0].Preview == nil {
		return "", false
	}
	if t := files[0].MIME; t != "text" && !strings.HasPrefix(t, "text/") {
		return "", false
	}
	return *files[0].Preview, true
}

// Offer is a transfer a sender proposes.
type Offer struct {
	SessionID string
	From      Peer
	// CertFingerprint is the fingerprint of the certificate the sender
	// proved it holds in the TLS handshake (unlike From.Fingerprint, it
	// can't be made up); "" without one.
	CertFingerprint string
	Files           []File
}

// Errors a Client returns, and a Handler returns to make a Server answer
// with the matching status.
var (
	ErrPINRequired  = errors.New("PIN required")
	ErrWrongPIN     = errors.New("wrong PIN")
	ErrTooManyTries = errors.New("too many attempts")
	ErrDeclined     = errors.New("declined")
	ErrBusy         = errors.New("busy with another transfer")
	ErrChecksum     = errors.New("checksum mismatch")
	ErrForbidden    = errors.New("invalid token or address")
	// ErrNothing: the receiver wants none of the files (or it read the
	// text message): there's nothing to upload.
	ErrNothing = errors.New("nothing to send")
	// ErrCertificate: the peer's certificate isn't the one its
	// fingerprint names.
	ErrCertificate = errors.New("certificate mismatch")
)

// StatusError is an unexpected answer from a peer.
type StatusError struct {
	Code    int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return "HTTP " + strconv.Itoa(e.Code) + ": " + e.Message
	}
	return "HTTP " + strconv.Itoa(e.Code)
}

// Session is an upload a receiver accepted: the files it wants, each
// with the token to upload it with.
type Session struct {
	ID     string
	Tokens map[string]string // file ID → token
}

// Client is the sending side of a protocol version. Its methods run
// over the http.Client given to the version's constructor, whose TLS
// configuration is the caller's (see Pinned).
type Client interface {
	// Register introduces self to the device at peer (whose Info may be
	// incomplete: a bare address while scanning) and returns its Info.
	Register(ctx context.Context, peer Peer, self Info) (Info, error)
	// PrepareUpload offers files to peer, waiting for its user to decide;
	// pin is "" until the peer asks for one (ErrPINRequired).
	PrepareUpload(ctx context.Context, peer Peer, self Info, files []File, pin string) (Session, error)
	// Upload sends the content of one accepted file.
	Upload(ctx context.Context, peer Peer, s Session, fileID string, body io.Reader, size int64) error
	// Cancel tells peer the sender gave up on the session.
	Cancel(ctx context.Context, peer Peer, sessionID string) error
}

// Handler is what a Server asks the application. Its methods are called
// from the server's goroutines, possibly at the same time.
type Handler interface {
	// Self is the device's own Info, answered to /register.
	Self() Info
	// Registered reports a device that introduced itself.
	Registered(p Peer)
	// Prepare asks whether to accept an offer, blocking until the user
	// decides — or ctx is done: the sender gave up. It returns the IDs
	// of the files wanted (none: nothing to upload), or ErrDeclined, or
	// ErrBusy.
	Prepare(ctx context.Context, o Offer) ([]string, error)
	// Receive saves the content of a file of an accepted session. body
	// fails with ErrChecksum at the end of a file whose SHA-256 isn't the
	// one announced: what was saved must then be removed.
	Receive(ctx context.Context, sessionID string, f File, body io.Reader) error
	// SessionEnded reports that a session the handler accepted is over:
	// every file arrived or failed, or the sender cancelled it.
	SessionEnded(sessionID string, cancelled bool)
	// CancelReceived reports a cancel for a session the server doesn't
	// know: the peer at ip, as a receiver, gives up a transfer the
	// application is sending it.
	CancelReceived(ip, sessionID string)
}

// Server is the receiving side of a protocol version: HTTP handlers for
// its endpoints, holding the sessions.
type Server interface {
	// Routes registers the version's endpoints on mux.
	Routes(mux *http.ServeMux)
	// EndSession drops a session the application gives up on (the user
	// cancelled the transfer): its files are refused from now on.
	EndSession(sessionID string)
}

// Announcement is a multicast discovery message.
type Announcement struct {
	Info
	// Announce: the device just started and wants answers; false for an
	// answer itself.
	Announce bool
}

// CertFingerprint is a certificate's fingerprint as LocalSend writes it:
// the SHA-256 of its DER encoding, uppercase hex.
func CertFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// SameFingerprint compares two fingerprints regardless of case.
func SameFingerprint(a, b string) bool { return strings.EqualFold(a, b) }

// IsCertFingerprint reports whether fp has the form of CertFingerprint's
// result (and so can be checked against a certificate), rather than a
// random string of a device without HTTPS.
func IsCertFingerprint(fp string) bool {
	if len(fp) != 64 {
		return false
	}
	_, err := hex.DecodeString(fp)
	return err == nil
}

// RequestPeer returns the address a request came from (an IPv4 one as
// such, not mapped into IPv6; a link-local IPv6 one with its zone) and
// the fingerprint of the client certificate it presented ("" if none).
func RequestPeer(r *http.Request) (ip, certFingerprint string) {
	ip = r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		ip = host
	}
	ip = strings.TrimPrefix(ip, "::ffff:")
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		certFingerprint = CertFingerprint(r.TLS.PeerCertificates[0].Raw)
	}
	return ip, certFingerprint
}

// VerifyPinned checks a server certificate chain against fingerprint, for
// tls.Config.VerifyPeerCertificate: devices use self-signed certificates,
// which only the fingerprint they announced can vouch for. A fingerprint
// that isn't a certificate's (a device first reached by address, while
// scanning) accepts any certificate.
func VerifyPinned(fingerprint string) func([][]byte, [][]*x509.Certificate) error {
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if !IsCertFingerprint(fingerprint) {
			return nil
		}
		if len(raw) == 0 || !SameFingerprint(CertFingerprint(raw[0]), fingerprint) {
			return ErrCertificate
		}
		return nil
	}
}
