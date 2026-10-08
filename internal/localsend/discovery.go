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
	"net"
	"sync"
	"time"

	"shfm/internal/applog"
	"shfm/internal/localsend/protocol"
	"shfm/internal/localsend/protocol/lsv2"
)

// announceDelays space an announcement's repetitions: a device that just
// started may not answer the first one yet.
var announceDelays = []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 2 * time.Second}

const (
	// scanTimeout bounds asking one address while scanning, and
	// registerTimeout answering an announcement.
	scanTimeout     = 2 * time.Second
	registerTimeout = 5 * time.Second
	// scanWorkers addresses are asked at the same time.
	scanWorkers = 64
	// maxScanHosts bounds the addresses scanned per network: a /24.
	maxScanHosts = 254
)

// Rescan forgets the devices found and looks for them again: an
// announcement, which every device answers, and the local networks'
// addresses asked one by one, for the devices multicast doesn't reach.
func (s *Service) Rescan() {
	s.mu.Lock()
	s.devices = map[string]Device{}
	already := s.scanning
	s.scanning = true
	s.mu.Unlock()
	s.changed()
	if s.mcast != nil {
		s.mcast.Rejoin()
	}
	s.announce()
	if already || localOnly {
		s.mu.Lock()
		s.scanning = already
		s.mu.Unlock()
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.scan()
		s.mu.Lock()
		s.scanning = false
		s.mu.Unlock()
		s.changed()
	}()
}

// announce sends the announcement burst, in the background.
func (s *Service) announce() {
	if s.mcast == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for _, d := range announceDelays {
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(d):
			}
			s.sendAnnouncement(true)
		}
	}()
}

func (s *Service) sendAnnouncement(announce bool) {
	msg := lsv2.EncodeAnnouncement(protocol.Announcement{Info: s.self(), Announce: announce})
	if err := s.mcast.Send(msg); err != nil {
		applog.Debug("localsend: announcement not sent", "error", err)
	}
}

// listenAnnouncements handles the multicast messages arriving on conn
// until the Service is closed: a device announcing itself gets an
// answer — over HTTP, or if that fails over multicast.
func (s *Service) listenAnnouncements(conn *net.UDPConn) {
	buf := make([]byte, 64<<10)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if s.ctx.Err() == nil {
				applog.Warn("localsend: multicast stopped", "error", err)
			}
			return
		}
		a, err := lsv2.DecodeAnnouncement(buf[:n])
		if err != nil || protocol.SameFingerprint(a.Fingerprint, s.fingerprint) || !lsv2.Speaks(a.Version) {
			continue
		}
		peer := protocol.Peer{Info: a.Info, IP: hostOf(from)}
		s.addDevice(peer)
		if a.Announce {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				ctx, cancel := context.WithTimeout(s.ctx, registerTimeout)
				defer cancel()
				if _, err := s.v2cli.Register(ctx, peer, s.self()); err != nil && s.ctx.Err() == nil {
					s.sendAnnouncement(false)
				}
			}()
		}
	}
}

// hostOf is the address a datagram came from: an IPv4 one as such, an
// IPv6 link-local one with its zone ("fe80::1%wlan0").
func hostOf(a *net.UDPAddr) string {
	if ip4 := a.IP.To4(); ip4 != nil {
		return ip4.String()
	}
	if a.Zone != "" {
		return a.IP.String() + "%" + a.Zone
	}
	return a.IP.String()
}

// scan asks every address of the local IPv4 networks (at most a /24
// around this device's address on each) whether LocalSend answers on
// the default port.
func (s *Service) scan() {
	hosts := make(chan net.IP)
	var wg sync.WaitGroup
	for range scanWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range hosts {
				s.probe(ip.String())
			}
		}()
	}
	defer func() {
		close(hosts)
		wg.Wait()
	}()
	for _, ip := range scanTargets() {
		select {
		case hosts <- ip:
		case <-s.ctx.Done():
			return
		}
	}
}

// probe registers with whatever answers at ip on the default port: over
// HTTPS, or HTTP for a device with encryption turned off.
func (s *Service) probe(ip string) {
	self := s.self()
	for _, https := range []bool{true, false} {
		peer := protocol.Peer{Info: protocol.Info{Port: protocol.DefaultPort, HTTPS: https}, IP: ip}
		ctx, cancel := context.WithTimeout(s.ctx, scanTimeout)
		info, err := s.v2cli.Register(ctx, peer, self)
		cancel()
		if err == nil {
			peer.Info = info
			s.addDevice(peer)
			return
		}
		// Only an HTTP server answering a TLS handshake is worth another
		// try over HTTP.
		var rec tls.RecordHeaderError
		if !errors.As(err, &rec) {
			return
		}
	}
}

// scanTargets lists the addresses to scan: the other hosts of every
// local IPv4 network, at most a /24 around this device's address.
func scanTargets() []net.IP {
	var out []net.IP
	seen := map[string]bool{}
	for _, n := range localNetworks() {
		ones, bits := n.Mask.Size()
		if bits != 32 || ones > 30 {
			continue
		}
		mask := n.Mask
		if ones < 24 {
			mask = net.CIDRMask(24, 32)
		}
		self := n.IP.To4()
		base := self.Mask(mask)
		size := 1 << (32 - maskOnes(mask))
		for i := 1; i < size-1 && i <= maxScanHosts; i++ {
			ip := make(net.IP, 4)
			copy(ip, base)
			ip[2] += byte(i >> 8)
			ip[3] += byte(i)
			if ip.Equal(self) || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			out = append(out, ip)
		}
	}
	return out
}

func maskOnes(m net.IPMask) int {
	ones, _ := m.Size()
	return ones
}

// localNetworks returns this device's IPv4 addresses, with their
// networks' masks, on the interfaces up that aren't loopback.
func localNetworks() []*net.IPNet {
	var out []*net.IPNet
	for _, ifi := range lanInterfaces() {
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLinkLocalUnicast() {
				out = append(out, &net.IPNet{IP: n.IP.To4(), Mask: n.Mask})
			}
		}
	}
	return out
}

// lanInterfaces6 returns the interfaces up, multicast capable, with an
// IPv6 address, that aren't loopback.
func lanInterfaces6() []net.Interface {
	ifis, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.Interface
	for _, ifi := range ifis {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() == nil && n.IP.To16() != nil {
				out = append(out, ifi)
				break
			}
		}
	}
	return out
}

// lanInterfaces returns the interfaces up, with an IPv4 address, that
// aren't loopback.
func lanInterfaces() []net.Interface {
	ifis, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []net.Interface
	for _, ifi := range ifis {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				out = append(out, ifi)
				break
			}
		}
	}
	return out
}
