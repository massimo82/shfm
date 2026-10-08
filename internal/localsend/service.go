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

//go:build localsend

package localsend

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"shfm/internal/applog"
	"shfm/internal/localsend/protocol"
	"shfm/internal/localsend/protocol/lsv2"
)

func init() { Available = true }

// deviceModel is what shfm announces as its model.
const deviceModel = "shfm"

// Service is LocalSend running: the device announced, its server
// listening, the devices found.
type Service struct {
	ev          Events
	cert        tls.Certificate
	fingerprint string
	port        int

	ln     net.Listener
	srv    *http.Server
	v2srv  *lsv2.Server
	v2cli  *lsv2.Client
	mcast  *multicast
	mcErr  error
	ctx    context.Context // done once closed
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu       sync.Mutex
	settings Settings
	devices  map[string]Device // by fingerprint
	clients  map[string]*http.Client
	recv     *receiveSession       // the transfer being received
	sends    map[*sending]struct{} // the transfers being sent
	pending  map[string]*request   // requests waiting for the user, by session
	scanning bool
}

// For tests: listenHost is the address the server listens on ("" for
// every one), and localOnly keeps the Service off the network — no
// multicast, no scanning.
var (
	listenHost string
	localOnly  bool
)

// Start starts LocalSend with the device identity kept in dir.
func Start(dir string, st Settings, ev Events) (*Service, error) {
	cert, fp, err := loadIdentity(dir)
	if err != nil {
		return nil, err
	}
	port := st.Port
	if port <= 0 {
		port = protocol.DefaultPort
	}
	// Another program (or shfm) on the port: devices learn the one used
	// from the announcements, only scanning needs the default one.
	ln, err := net.Listen("tcp", net.JoinHostPort(listenHost, strconv.Itoa(port)))
	if err != nil {
		ln, err = net.Listen("tcp", net.JoinHostPort(listenHost, "0"))
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		ev: ev, cert: cert, fingerprint: fp,
		port:     ln.Addr().(*net.TCPAddr).Port,
		ln:       ln,
		ctx:      ctx,
		cancel:   cancel,
		settings: st,
		devices:  map[string]Device{},
		clients:  map[string]*http.Client{},
		sends:    map[*sending]struct{}{},
		pending:  map[string]*request{},
	}
	s.v2srv = lsv2.NewServer(handler{s}, s.pin)
	s.v2cli = &lsv2.Client{HTTP: s.httpClient}
	mux := http.NewServeMux()
	s.v2srv.Routes(mux)
	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// TLS handshakes failing (a scanner, a browser) are logged by the
		// server: never on the terminal, which is shfm's screen.
		ErrorLog: log.New(logWriter{}, "", 0),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			// Senders prove who they are with their certificate, which
			// only their fingerprint vouches for: any is asked, none
			// checked here (see protocol.RequestPeer).
			ClientAuth: tls.RequestClientCert,
			MinVersion: tls.VersionTLS12,
		},
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.srv.ServeTLS(ln, "", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			applog.Warn("localsend: server stopped", "error", err)
		}
	}()

	if localOnly {
		s.mcErr = errors.New("off for tests")
	} else {
		s.mcast, s.mcErr = listenMulticast(port)
	}
	if s.mcErr != nil {
		applog.Warn("localsend: multicast not available", "error", s.mcErr)
	} else {
		for _, conn := range s.mcast.Conns() {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.listenAnnouncements(conn)
			}()
		}
	}
	s.Rescan()
	return s, nil
}

// Close stops the Service: the server, the announcements, and every
// transfer.
func (s *Service) Close() {
	// Requests waiting for the user and transfers going on stop with
	// the context.
	s.cancel()
	shutdown, done := context.WithTimeout(context.Background(), 2*time.Second)
	defer done()
	s.srv.Shutdown(shutdown)
	s.srv.Close()
	if s.mcast != nil {
		s.mcast.Close()
	}
	s.wg.Wait()
}

// Status returns the Service's state.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{
		Alias: s.alias(), Port: s.port, Fingerprint: s.fingerprint,
		Receive: s.settings.Receive, PIN: s.settings.PIN != "", Multicast: s.mcErr,
	}
}

// Update applies new settings (but the port, used from the next start).
func (s *Service) Update(st Settings) {
	s.mu.Lock()
	old := s.settings
	st.Port = old.Port
	s.settings = st
	s.mu.Unlock()
	if st.Alias != old.Alias {
		s.announce()
	}
	s.changed()
}

// Scanning reports whether a scan for devices is going on.
func (s *Service) Scanning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanning
}

// Devices returns the devices found, by name.
func (s *Service) Devices() []Device {
	s.mu.Lock()
	out := make([]Device, 0, len(s.devices))
	for _, d := range s.devices {
		out = append(out, d)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Alias != out[j].Alias {
			return out[i].Alias < out[j].Alias
		}
		return out[i].IP < out[j].IP
	})
	return out
}

// alias is the name announced; s.mu held.
func (s *Service) alias() string {
	if s.settings.Alias != "" {
		return s.settings.Alias
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "shfm"
}

func (s *Service) pin() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.PIN
}

// self is the device's own info.
func (s *Service) self() protocol.Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return protocol.Info{
		Alias: s.alias(), Version: lsv2.Version, DeviceModel: deviceModel, DeviceType: protocol.TypeDesktop,
		Fingerprint: s.fingerprint, Port: s.port, HTTPS: true,
	}
}

func (s *Service) changed() {
	if s.ev.Changed != nil {
		s.ev.Changed()
	}
}

// addDevice records a device found (again).
func (s *Service) addDevice(p protocol.Peer) {
	if p.Fingerprint == "" || protocol.SameFingerprint(p.Fingerprint, s.fingerprint) || !lsv2.Speaks(p.Version) {
		return
	}
	d := Device{
		Alias: p.Alias, Model: p.DeviceModel, Type: p.DeviceType, Fingerprint: p.Fingerprint,
		IP: p.IP, Port: p.Port, HTTPS: p.HTTPS, Version: p.Version,
	}
	if d.Alias == "" {
		d.Alias = p.IP
	}
	s.mu.Lock()
	old, known := s.devices[d.Fingerprint]
	// A device announcing over IPv4 and IPv6 is reached over IPv4: an
	// address that doesn't depend on the interface it was heard on.
	if known && isIPv4(old.IP) && !isIPv4(d.IP) {
		d.IP, d.Port, d.HTTPS = old.IP, old.Port, old.HTTPS
	}
	s.devices[d.Fingerprint] = d
	s.mu.Unlock()
	if !known || old != d {
		s.changed()
	}
}

func isIPv4(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.To4() != nil
}

func peerOf(d Device) protocol.Peer {
	return protocol.Peer{
		Info: protocol.Info{Alias: d.Alias, Version: d.Version, DeviceModel: d.Model, DeviceType: d.Type,
			Fingerprint: d.Fingerprint, Port: d.Port, HTTPS: d.HTTPS},
		IP: d.IP,
	}
}

func deviceOf(p protocol.Peer) Device {
	d := Device{Alias: p.Alias, Model: p.DeviceModel, Type: p.DeviceType, Fingerprint: p.Fingerprint,
		IP: p.IP, Port: p.Port, HTTPS: p.HTTPS, Version: p.Version}
	if d.Alias == "" {
		d.Alias = p.IP
	}
	return d
}

// httpClient returns the client to reach peer with: its certificate
// pinned to the fingerprint it announced, this device's presented.
func (s *Service) httpClient(peer protocol.Peer) *http.Client {
	key := peer.IP + "|" + strconv.Itoa(peer.Port) + "|" + peer.Fingerprint
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[key]; ok {
		return c
	}
	cert := s.cert
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second,
		IdleConnTimeout:     30 * time.Second,
		TLSClientConfig: &tls.Config{
			// Self-signed: checked against the fingerprint instead.
			InsecureSkipVerify:    true,
			VerifyPeerCertificate: protocol.VerifyPinned(peer.Fingerprint),
			GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
				return &cert, nil
			},
			MinVersion: tls.VersionTLS12,
		},
	}
	c := &http.Client{Transport: tr}
	if len(s.clients) > 64 {
		for k, old := range s.clients {
			old.CloseIdleConnections()
			delete(s.clients, k)
		}
	}
	s.clients[key] = c
	return c
}

// handler is the Service as the protocol servers' protocol.Handler.
type handler struct{ s *Service }

func (h handler) Self() protocol.Info { return h.s.self() }

func (h handler) Registered(p protocol.Peer) { h.s.addDevice(p) }

func (h handler) Prepare(ctx context.Context, o protocol.Offer) ([]string, error) {
	return h.s.prepare(ctx, o)
}

func (h handler) Receive(ctx context.Context, sessionID string, f protocol.File, body io.Reader) error {
	return h.s.receive(ctx, sessionID, f, body)
}

func (h handler) SessionEnded(sessionID string, cancelled bool) {
	h.s.sessionEnded(sessionID, cancelled)
}

func (h handler) CancelReceived(ip, sessionID string) { h.s.cancelReceived(ip, sessionID) }

// logWriter sends a log.Logger's lines to shfm's log.
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	applog.Debug("localsend: http server", "message", strings.TrimSpace(string(p)))
	return len(p), nil
}
