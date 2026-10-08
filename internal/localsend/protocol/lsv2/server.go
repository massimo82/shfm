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
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"net/http"
	"sync"

	"shfm/internal/localsend/protocol"
)

// MaxPINAttempts is how many wrong PINs an address may try: then it's
// refused (429) for as long as the server runs, as by the reference
// implementation.
const MaxPINAttempts = 3

// maxRequest bounds a prepare-upload request (the file list).
const maxRequest = 16 << 20

// Server is the receiving side of version 2. It holds at most one
// session at a time, as the protocol asks: another sender gets 409.
type Server struct {
	h protocol.Handler
	// PIN returns the PIN senders must give, "" for none.
	pin func() string

	mu       sync.Mutex
	session  *session       // the pending or active session, nil if none
	attempts map[string]int // wrong PINs by address
}

var _ protocol.Server = (*Server)(nil)

type fileState int

const (
	filePending fileState = iota
	fileInProgress
	fileDone
	fileFailed
)

type sessionFile struct {
	file  protocol.File
	token string
	state fileState
}

type session struct {
	id     string
	ip     string
	active bool // accepted: before, the user is still deciding
	files  map[string]*sessionFile
	// cancel stops a pending prepare-upload (the sender cancelled it).
	cancel context.CancelFunc
}

// NewServer returns a server asking h, and pin for the PIN.
func NewServer(h protocol.Handler, pin func() string) *Server {
	return &Server{h: h, pin: pin, attempts: map[string]int{}}
}

// Routes registers the version 2 endpoints on mux.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+pathRegister, s.register)
	mux.HandleFunc("GET "+pathInfo, s.info)
	mux.HandleFunc("POST "+pathPrepareUpload, s.prepareUpload)
	mux.HandleFunc("POST "+pathUpload, s.upload)
	mux.HandleFunc("POST "+pathCancel, s.cancel)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var d registerDTO
	if err := json.NewDecoder(io.LimitReader(r.Body, maxAnswer)).Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid body")
		return
	}
	ip, certFP := protocol.RequestPeer(r)
	// Over TLS, a device is only trusted to be what its certificate
	// proves.
	if d.Fingerprint != "" && (certFP == "" || protocol.SameFingerprint(certFP, d.Fingerprint)) {
		s.h.Registered(protocol.Peer{Info: d.info(), IP: ip})
	}
	writeJSON(w, http.StatusOK, toInfoDTO(s.h.Self()))
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, toInfoDTO(s.h.Self()))
}

// checkPIN answers false (having written the error) when the request
// doesn't carry the PIN needed.
func (s *Server) checkPIN(w http.ResponseWriter, r *http.Request, ip string) bool {
	want := s.pin()
	if want == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempts[ip] >= MaxPINAttempts {
		writeError(w, http.StatusTooManyRequests, "Too many requests")
		return false
	}
	got, ok := r.URL.Query()["pin"]
	switch {
	case !ok:
		writeError(w, http.StatusUnauthorized, "PIN required")
		return false
	case subtle.ConstantTimeCompare([]byte(got[0]), []byte(want)) == 1:
		delete(s.attempts, ip)
		return true
	}
	s.attempts[ip]++
	writeError(w, http.StatusUnauthorized, "Invalid PIN")
	return false
}

func (s *Server) prepareUpload(w http.ResponseWriter, r *http.Request) {
	ip, certFP := protocol.RequestPeer(r)
	if !s.checkPIN(w, r, ip) {
		return
	}
	var req prepareUploadRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequest)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid body")
		return
	}
	if len(req.Files) == 0 {
		writeError(w, http.StatusBadRequest, "No files provided")
		return
	}
	files := make(map[string]*sessionFile, len(req.Files))
	offer := protocol.Offer{
		From:            protocol.Peer{Info: req.Info.info(), IP: ip},
		CertFingerprint: certFP,
	}
	for id, d := range req.Files {
		f := d.file()
		f.ID = id
		if f.Size < 0 {
			writeError(w, http.StatusBadRequest, "Invalid file size")
			return
		}
		files[id] = &sessionFile{file: f}
		offer.Files = append(offer.Files, f)
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sess := &session{id: randomID(), ip: ip, files: files, cancel: cancel}
	offer.SessionID = sess.id
	s.mu.Lock()
	if s.session != nil {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "Blocked by another session")
		return
	}
	s.session = sess
	s.mu.Unlock()

	accepted, err := s.h.Prepare(ctx, offer)
	if err == nil && ctx.Err() != nil {
		err = protocol.ErrDeclined // the sender cancelled while the user decided
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := map[string]string{}
	if err == nil {
		for _, id := range accepted {
			if f, ok := files[id]; ok {
				f.token = randomID()
				keep[id] = f.token
			}
		}
	}
	if err != nil || len(keep) == 0 {
		if s.session == sess {
			s.session = nil
		}
		switch {
		case errors.Is(err, protocol.ErrBusy):
			writeError(w, http.StatusConflict, "Blocked by another session")
		case err != nil:
			writeError(w, http.StatusForbidden, "Rejected")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	for id := range files {
		if _, ok := keep[id]; !ok {
			delete(files, id)
		}
	}
	sess.active = true
	writeJSON(w, http.StatusOK, prepareUploadResponse{SessionID: sess.id, Files: keep})
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sid, fid, token := q.Get("sessionId"), q.Get("fileId"), q.Get("token")
	if sid == "" || fid == "" || token == "" {
		writeError(w, http.StatusBadRequest, "Missing parameters")
		return
	}
	ip, _ := protocol.RequestPeer(r)
	s.mu.Lock()
	sess := s.session
	var f *sessionFile
	if sess != nil && sess.active && sess.id == sid && sess.ip == ip {
		f = sess.files[fid]
	}
	if f == nil || f.state != filePending || subtle.ConstantTimeCompare([]byte(f.token), []byte(token)) != 1 {
		s.mu.Unlock()
		writeError(w, http.StatusForbidden, "Invalid token or IP address")
		return
	}
	f.state = fileInProgress
	file := f.file
	s.mu.Unlock()

	body := &checkedBody{r: r.Body, size: file.Size}
	if file.SHA256 != "" {
		body.hash, body.want = sha256.New(), file.SHA256
	}
	err := s.h.Receive(r.Context(), sid, file, body)

	s.mu.Lock()
	f.state = fileDone
	if err != nil {
		f.state = fileFailed
	}
	ended := false
	if s.session == sess {
		ended = true
		for _, f := range sess.files {
			if f.state == filePending || f.state == fileInProgress {
				ended = false
			}
		}
		if ended {
			s.session = nil
		}
	}
	s.mu.Unlock()
	if ended {
		s.h.SessionEnded(sid, false)
	}
	switch {
	case err == nil:
		w.WriteHeader(http.StatusOK)
	case errors.Is(err, protocol.ErrChecksum):
		writeError(w, http.StatusUnprocessableEntity, "Checksum mismatch")
	default:
		writeError(w, http.StatusInternalServerError, "Could not save the file")
	}
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sessionId")
	ip, _ := protocol.RequestPeer(r)
	s.mu.Lock()
	sess := s.session
	switch {
	case sess != nil && !sess.active && sess.ip == ip && (sid == "" || sid == sess.id):
		// A pending request: the sender doesn't know the session's ID yet.
		sess.cancel()
		s.mu.Unlock()
	case sess != nil && sess.active && sess.ip == ip && sid == sess.id:
		s.session = nil
		s.mu.Unlock()
		s.h.SessionEnded(sid, true)
	default:
		s.mu.Unlock()
		if sid != "" {
			s.h.CancelReceived(ip, sid)
		}
	}
	w.WriteHeader(http.StatusOK)
}

// EndSession drops the session sessionID, if it's still the current one.
func (s *Server) EndSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil && s.session.id == sessionID {
		if !s.session.active {
			s.session.cancel()
		}
		s.session = nil
	}
}

// checkedBody is an upload's body, failing unless it is exactly size
// bytes long, and with ErrChecksum at its end when its SHA-256 isn't
// want's.
type checkedBody struct {
	r    io.Reader
	size int64
	n    int64
	hash hash.Hash
	want string
}

var errSize = errors.New("the file's size isn't the one announced")

func (b *checkedBody) Read(p []byte) (int, error) {
	if left := b.size - b.n + 1; int64(len(p)) > left {
		p = p[:left] // one more byte than expected is enough to tell
	}
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.hash != nil {
		b.hash.Write(p[:n])
	}
	if b.n > b.size {
		return n, errSize
	}
	if err == io.EOF {
		if b.n != b.size {
			return n, errSize
		}
		if b.hash != nil && hex.EncodeToString(b.hash.Sum(nil)) != b.want {
			return n, protocol.ErrChecksum
		}
	}
	return n, err
}

func randomID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
