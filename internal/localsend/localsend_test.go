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
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"shfm/internal/fileops"
	"shfm/internal/localsend/protocol"
	"shfm/internal/vfs"
)

func init() {
	// Two services on this computer's loopback only: nothing reaches the
	// network.
	listenHost, localOnly = "127.0.0.1", true
}

type peerPair struct {
	sender, receiver *Service
	recvDir          string
	incoming         chan *Request
}

// newPair starts a sender and a receiver that know each other, the
// receiver answering requests through incoming. Port 1 can't be listened
// on: each gets a free port.
func newPair(t *testing.T, pin string) *peerPair {
	t.Helper()
	return newPairOn(t, "127.0.0.1", pin)
}

// newPairOn is newPair with both on the loopback address host.
func newPairOn(t *testing.T, host, pin string) *peerPair {
	t.Helper()
	saved := listenHost
	listenHost = host
	defer func() { listenHost = saved }()
	p := &peerPair{recvDir: t.TempDir(), incoming: make(chan *Request, 4)}
	var err error
	p.receiver, err = Start(t.TempDir(), Settings{Alias: "receiver", Port: 1, Receive: true, PIN: pin},
		Events{Incoming: func(r *Request) { p.incoming <- r }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.receiver.Close)
	p.sender, err = Start(t.TempDir(), Settings{Alias: "sender", Port: 1}, Events{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.sender.Close)
	st := p.receiver.Status()
	peer := protocol.Peer{Info: protocol.Info{Port: st.Port, HTTPS: true, Fingerprint: st.Fingerprint}, IP: host}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := p.sender.v2cli.Register(ctx, peer, p.sender.self())
	if err != nil {
		t.Fatal(err)
	}
	peer.Info = info
	p.sender.addDevice(peer)
	return p
}

func (p *peerPair) device(t *testing.T) Device {
	t.Helper()
	devs := p.sender.Devices()
	if len(devs) != 1 || devs[0].Alias != "receiver" {
		t.Fatalf("devices = %+v", devs)
	}
	return devs[0]
}

// acceptAll accepts the next request into the receiver's folder.
func (p *peerPair) acceptAll(t *testing.T) chan *fileops.Result {
	done := make(chan *fileops.Result, 1)
	go func() {
		select {
		case r := <-p.incoming:
			done <- r.Accept(vfs.NewLocalFS("Local", p.recvDir), p.recvDir, nil)
		case <-time.After(10 * time.Second):
			done <- &fileops.Result{Errors: []error{os.ErrDeadlineExceeded}}
		}
	}()
	return done
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func checkFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}

func TestSendFilesAndFolders(t *testing.T) {
	p := newPair(t, "")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	writeFile(t, filepath.Join(src, "Photos", "2024", "b.jpg"), strings.Repeat("b", 1<<20))
	writeFile(t, filepath.Join(p.recvDir, "a.txt"), "already there")
	fs := vfs.NewLocalFS("Local", src)
	items := []fileops.Item{{FS: fs, Path: filepath.Join(src, "a.txt")}, {FS: fs, Path: filepath.Join(src, "Photos")}}

	done := p.acceptAll(t)
	res := p.sender.Send(p.device(t), items, nil, nil)
	if len(res.Errors) != 0 || res.Done != 2 {
		t.Fatalf("send: %+v", res)
	}
	if r := <-done; len(r.Errors) != 0 || r.Done != 2 {
		t.Fatalf("receive: %+v", r)
	}
	checkFile(t, filepath.Join(p.recvDir, "a.txt"), "already there")
	checkFile(t, filepath.Join(p.recvDir, "a (2).txt"), "alpha")
	checkFile(t, filepath.Join(p.recvDir, "Photos", "2024", "b.jpg"), strings.Repeat("b", 1<<20))
}

func TestDeclined(t *testing.T) {
	p := newPair(t, "")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	go func() { (<-p.incoming).Decline() }()
	res := p.sender.Send(p.device(t), []fileops.Item{{FS: vfs.NewLocalFS("Local", src), Path: filepath.Join(src, "a.txt")}}, nil, nil)
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "declined") {
		t.Fatalf("send: %+v", res)
	}
}

func TestPIN(t *testing.T) {
	p := newPair(t, "1234")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	items := []fileops.Item{{FS: vfs.NewLocalFS("Local", src), Path: filepath.Join(src, "a.txt")}}

	asked := 0
	ask := func(dev Device, wrong bool) (string, bool) {
		asked++
		if asked == 1 {
			return "0000", true
		}
		return "1234", true
	}
	done := p.acceptAll(t)
	res := p.sender.Send(p.device(t), items, ask, nil)
	if len(res.Errors) != 0 || asked != 2 {
		t.Fatalf("send: %+v, asked %d times", res, asked)
	}
	if r := <-done; len(r.Errors) != 0 {
		t.Fatalf("receive: %+v", r)
	}
	checkFile(t, filepath.Join(p.recvDir, "a.txt"), "alpha")

	// Without a PIN, nothing is offered.
	res = p.sender.Send(p.device(t), items, func(Device, bool) (string, bool) { return "", false }, nil)
	if len(res.Errors) != 1 {
		t.Fatalf("send without PIN: %+v", res)
	}
}

func TestMessage(t *testing.T) {
	p := newPair(t, "")
	res := p.sender.SendText(p.device(t), "hello there", nil, nil)
	if len(res.Errors) != 0 {
		t.Fatalf("send: %+v", res)
	}
	select {
	case r := <-p.incoming:
		if !r.IsMessage || r.Message != "hello there" || r.From.Alias != "sender" {
			t.Fatalf("request = %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
}

func TestReceiveOff(t *testing.T) {
	p := newPair(t, "")
	p.receiver.Update(Settings{Alias: "receiver"})
	res := p.sender.SendText(p.device(t), "hi", nil, nil)
	if len(res.Errors) != 1 {
		t.Fatalf("send: %+v", res)
	}
}

func TestCancelBySender(t *testing.T) {
	p := newPair(t, "")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	items := []fileops.Item{{FS: vfs.NewLocalFS("Local", src), Path: filepath.Join(src, "a.txt")}}
	var stop atomic.Bool
	prog := &fileops.Progress{Cancelled: stop.Load}
	go func() {
		r := <-p.incoming
		stop.Store(true) // the sender gives up while the receiver decides
		time.Sleep(time.Second)
		if res := r.Accept(vfs.NewLocalFS("Local", p.recvDir), p.recvDir, nil); len(res.Errors) == 0 {
			t.Error("accepted a withdrawn request")
		}
	}()
	res := p.sender.Send(p.device(t), items, nil, prog)
	if !res.Cancelled {
		t.Fatalf("send: %+v", res)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(p.recvDir, "a.txt")); err == nil {
		t.Fatal("file received anyway")
	}
}

// Over IPv6: the server, the addresses checked between a session's
// requests, the URLs.
func TestSendOverIPv6(t *testing.T) {
	if ln, err := net.Listen("tcp", "[::1]:0"); err != nil {
		t.Skip("no IPv6 loopback:", err)
	} else {
		ln.Close()
	}
	p := newPairOn(t, "::1", "")
	if dev := p.device(t); dev.IP != "::1" {
		t.Fatalf("device reached at %q", dev.IP)
	}
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	done := p.acceptAll(t)
	res := p.sender.Send(p.device(t), []fileops.Item{{FS: vfs.NewLocalFS("Local", src), Path: filepath.Join(src, "a.txt")}}, nil, nil)
	if len(res.Errors) != 0 || res.Done != 1 {
		t.Fatalf("send: %+v", res)
	}
	if r := <-done; len(r.Errors) != 0 {
		t.Fatalf("receive: %+v", r)
	}
	checkFile(t, filepath.Join(p.recvDir, "a.txt"), "alpha")
}

// A device heard over IPv6 too keeps its IPv4 address.
func TestPrefersIPv4(t *testing.T) {
	p := newPair(t, "")
	dev := p.device(t)
	p.sender.addDevice(protocol.Peer{Info: peerOf(dev).Info, IP: "fe80::1%eth0"})
	if got := p.device(t); got.IP != "127.0.0.1" {
		t.Fatalf("device now at %q", got.IP)
	}
}
