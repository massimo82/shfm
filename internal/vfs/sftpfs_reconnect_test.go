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

package vfs

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// droppingSFTPServer is an in-process SFTP server that can drop every
// open connection, like a server ending idle sessions.
type droppingSFTPServer struct {
	mu    sync.Mutex
	conns []net.Conn
	port  int
}

func startDroppingSFTPServer(t *testing.T) *droppingSFTPServer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "u" && string(pass) == "pw" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &droppingSFTPServer{port: ln.Addr().(*net.TCPAddr).Port}
	t.Cleanup(func() { ln.Close(); srv.dropAll() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			srv.mu.Lock()
			srv.conns = append(srv.conns, conn)
			srv.mu.Unlock()
			go serveSFTPConn(conn, cfg)
		}
	}()
	return srv
}

func (s *droppingSFTPServer) dropAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.Close()
	}
	s.conns = nil
}

func serveSFTPConn(conn net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range chReqs {
				ok := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
				if ok {
					if srv, err := sftp.NewServer(ch); err == nil {
						srv.Serve()
					}
					ch.Close()
				}
			}
		}()
	}
}

// A pane left open on a share whose server has since dropped the
// connection keeps working: the next operation reconnects.
func TestSFTPReconnectsAfterServerDrop(t *testing.T) {
	// DialSFTP records the host key in ~/.ssh/known_hosts.
	t.Setenv("HOME", t.TempDir())
	old := sessionRedialInterval
	sessionRedialInterval = 0
	t.Cleanup(func() { sessionRedialInterval = old })

	srv := startDroppingSFTPServer(t)
	fs, err := DialSFTP(SFTPOptions{Host: "127.0.0.1", Port: srv.port, User: "u", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "film.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.List(dir); err != nil {
		t.Fatal(err)
	}

	srv.dropAll()

	entries, err := fs.List(dir)
	if err != nil {
		t.Fatalf("List after the server dropped the connection = %v, want a transparent reconnection", err)
	}
	if len(entries) != 1 || entries[0].Name != "film.mkv" {
		t.Fatalf("List = %+v, want film.mkv", entries)
	}
	if err := fs.Mkdir(filepath.Join(dir, "sub")); err != nil {
		t.Fatalf("Mkdir on the new connection = %v", err)
	}
}
