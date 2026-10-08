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

//go:build localsend && linux

package localsend

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"

	"shfm/internal/localsend/protocol"
)

// multicast is the announcements' UDP groups — IPv4's, and IPv6's
// alongside it — joined on every network interface: a computer on Wi-Fi
// and Ethernet at once, or with a VPN, must be found from both.
type multicast struct {
	port int
	// v4 and v6 are nil when that family isn't available.
	v4, v6 *net.UDPConn
	send4  *net.UDPConn
}

// reuseAddr lets other LocalSend programs on this computer listen on the
// same port, as the reference implementation does.
func reuseAddr(v6 bool) net.ListenConfig {
	return net.ListenConfig{Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		err := rc.Control(func(fd uintptr) {
			serr = errors.Join(
				unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1),
				unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1))
			if v6 {
				serr = errors.Join(serr, unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_V6ONLY, 1))
			}
		})
		return errors.Join(err, serr)
	}}
}

// listenMulticast joins the groups. It fails only when neither family
// works. The groups are joined on each interface by its index:
// net.ListenMulticastUDP joins through the default route, and fails
// without one (a network with no internet access).
func listenMulticast(port int) (*multicast, error) {
	m := &multicast{port: port}
	err4 := m.listen4()
	err6 := m.listen6()
	if m.v4 == nil && m.v6 == nil {
		return nil, errors.Join(err4, err6)
	}
	return m, nil
}

func (m *multicast) listen4() error {
	lc := reuseAddr(false)
	pc, err := lc.ListenPacket(context.Background(), "udp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(m.port)))
	if err != nil {
		return err
	}
	recv := pc.(*net.UDPConn)
	if err := joinAll(lanInterfaces(), func(ifi net.Interface) error { return join4(recv, ifi.Index) }); err != nil {
		recv.Close()
		return err
	}
	send, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		recv.Close()
		return err
	}
	// One hop: the announcements are for the local network only; other
	// programs on this computer hear them too.
	control(send, func(fd int) error {
		return errors.Join(
			unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_MULTICAST_LOOP, 1),
			unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_MULTICAST_TTL, 1))
	})
	m.v4, m.send4 = recv, send
	return nil
}

func (m *multicast) listen6() error {
	lc := reuseAddr(true)
	pc, err := lc.ListenPacket(context.Background(), "udp6", net.JoinHostPort("::", strconv.Itoa(m.port)))
	if err != nil {
		return err
	}
	conn := pc.(*net.UDPConn)
	if err := joinAll(lanInterfaces6(), func(ifi net.Interface) error { return join6(conn, ifi.Index) }); err != nil {
		conn.Close()
		return err
	}
	control(conn, func(fd int) error {
		return errors.Join(
			unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_LOOP, 1),
			unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_HOPS, 1))
	})
	m.v6 = conn
	return nil
}

// joinAll joins a group on every interface of ifis: an error only when
// it couldn't on any.
func joinAll(ifis []net.Interface, join func(net.Interface) error) error {
	if len(ifis) == 0 {
		return errors.New("no network interface")
	}
	var errs []error
	for _, ifi := range ifis {
		if err := join(ifi); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ifi.Name, err))
		}
	}
	if len(errs) == len(ifis) {
		return errors.Join(errs...)
	}
	return nil
}

func control(c *net.UDPConn, f func(fd int) error) error {
	rc, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	err = rc.Control(func(fd uintptr) { serr = f(int(fd)) })
	return errors.Join(err, serr)
}

func join4(c *net.UDPConn, ifindex int) error {
	mreq := &unix.IPMreqn{Ifindex: int32(ifindex)}
	copy(mreq.Multiaddr[:], net.ParseIP(protocol.MulticastGroup).To4())
	return control(c, func(fd int) error {
		return unix.SetsockoptIPMreqn(fd, unix.IPPROTO_IP, unix.IP_ADD_MEMBERSHIP, mreq)
	})
}

func join6(c *net.UDPConn, ifindex int) error {
	mreq := &unix.IPv6Mreq{Interface: uint32(ifindex)}
	copy(mreq.Multiaddr[:], net.ParseIP(protocol.MulticastGroup6))
	return control(c, func(fd int) error {
		return unix.SetsockoptIPv6Mreq(fd, unix.IPPROTO_IPV6, unix.IPV6_JOIN_GROUP, mreq)
	})
}

// Rejoin joins the groups on the interfaces that came up since (a Wi-Fi
// connected later); those already joined refuse, harmlessly.
func (m *multicast) Rejoin() {
	if m.v4 != nil {
		for _, ifi := range lanInterfaces() {
			join4(m.v4, ifi.Index)
		}
	}
	if m.v6 != nil {
		for _, ifi := range lanInterfaces6() {
			join6(m.v6, ifi.Index)
		}
	}
}

// Send sends msg to the groups on every interface.
func (m *multicast) Send(msg []byte) error {
	var errs []error
	sent := false
	if m.send4 != nil {
		group := &net.UDPAddr{IP: net.ParseIP(protocol.MulticastGroup), Port: m.port}
		for _, ifi := range lanInterfaces() {
			err := control(m.send4, func(fd int) error {
				return unix.SetsockoptIPMreqn(fd, unix.IPPROTO_IP, unix.IP_MULTICAST_IF, &unix.IPMreqn{Ifindex: int32(ifi.Index)})
			})
			if err == nil {
				_, err = m.send4.WriteToUDP(msg, group)
			}
			if err != nil {
				errs = append(errs, err)
			} else {
				sent = true
			}
		}
	}
	if m.v6 != nil {
		for _, ifi := range lanInterfaces6() {
			// The zone picks the interface of a link-local group.
			group := &net.UDPAddr{IP: net.ParseIP(protocol.MulticastGroup6), Port: m.port, Zone: ifi.Name}
			if _, err := m.v6.WriteToUDP(msg, group); err != nil {
				errs = append(errs, err)
			} else {
				sent = true
			}
		}
	}
	if !sent {
		if len(errs) == 0 {
			return errors.New("no network interface")
		}
		return errors.Join(errs...)
	}
	return nil
}

// Conns returns the connections the announcements arrive on.
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
