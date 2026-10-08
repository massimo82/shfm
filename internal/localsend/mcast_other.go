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

//go:build localsend && !linux

package localsend

import (
	"errors"
	"net"

	"shfm/internal/localsend/protocol"
)

// multicast is the announcements' UDP groups, IPv4's and IPv6's, on the
// default interface only (joining every interface is done on Linux).
type multicast struct {
	port   int
	v4, v6 *net.UDPConn
	send4  *net.UDPConn
}

func listenMulticast(port int) (*multicast, error) {
	m := &multicast{port: port}
	v4, err4 := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP(protocol.MulticastGroup), Port: port})
	if err4 == nil {
		send, err := net.ListenUDP("udp4", &net.UDPAddr{})
		if err != nil {
			v4.Close()
			err4 = err
		} else {
			m.v4, m.send4 = v4, send
		}
	}
	v6, err6 := net.ListenMulticastUDP("udp6", nil, &net.UDPAddr{IP: net.ParseIP(protocol.MulticastGroup6), Port: port})
	if err6 == nil {
		m.v6 = v6
	}
	if m.v4 == nil && m.v6 == nil {
		return nil, errors.Join(err4, err6)
	}
	return m, nil
}

func (m *multicast) Rejoin() {}

func (m *multicast) Send(msg []byte) error {
	var errs []error
	if m.send4 != nil {
		_, err := m.send4.WriteToUDP(msg, &net.UDPAddr{IP: net.ParseIP(protocol.MulticastGroup), Port: m.port})
		errs = append(errs, err)
	}
	if m.v6 != nil {
		_, err := m.v6.WriteToUDP(msg, &net.UDPAddr{IP: net.ParseIP(protocol.MulticastGroup6), Port: m.port})
		errs = append(errs, err)
	}
	for _, err := range errs {
		if err == nil {
			return nil
		}
	}
	return errors.Join(errs...)
}

func (m *multicast) Conns() []*net.UDPConn {
	var out []*net.UDPConn
	for _, c := range []*net.UDPConn{m.v4, m.v6} {
		if c != nil {
			out = append(out, c)
		}
	}
	return out
}

func (m *multicast) Close() {
	for _, c := range []*net.UDPConn{m.v4, m.v6, m.send4} {
		if c != nil {
			c.Close()
		}
	}
}
