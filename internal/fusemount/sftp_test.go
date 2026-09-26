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

//go:build linux

package fusemount

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"shfm/internal/vfs"
)

// startSFTPServer runs an in-process SSH server with the SFTP subsystem on
// a loopback port, accepting user "u" with password "pw", and returns its
// port.
func startSFTPServer(t *testing.T) int {
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
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSH(conn, cfg)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func serveSSH(conn net.Conn, cfg *ssh.ServerConfig) {
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

// TestSFTPMount mounts a real SFTPFS (against the in-process server) and
// checks reads, seeks, writes and the editor-style save by rename go all
// the way through to the served folder.
func TestSFTPMount(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skip("no /dev/fuse")
	}
	// DialSFTP records the server's host key in ~/.ssh/known_hosts: point
	// it at a throwaway home.
	t.Setenv("HOME", t.TempDir())
	port := startSFTPServer(t)
	src, err := vfs.DialSFTP(vfs.SFTPOptions{Host: "127.0.0.1", Port: port, User: "u", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	served := t.TempDir() // an absolute path, the same on the server
	data := make([]byte, 1<<20+3)
	for i := range data {
		data[i] = byte(i * 13)
	}
	if err := os.WriteFile(filepath.Join(served, "film.avi"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	mg := NewManager(t.TempDir())
	defer mg.UnmountAll()
	local, err := mg.LocalPath(src, filepath.ToSlash(filepath.Join(served, "film.avi")))
	if err != nil {
		t.Skipf("FUSE mount not available here: %v", err)
	}
	// The UI closing its own connection must not affect the mount.
	src.Close()

	got, err := os.ReadFile(local)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("ReadFile through the mount: err %v, equal %v", err, bytes.Equal(got, data))
	}
	f, err := os.Open(local)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if n, err := f.ReadAt(buf, 700000); err != nil || !bytes.Equal(buf[:n], data[700000:700064]) {
		t.Fatalf("ReadAt = %d, %v", n, err)
	}
	f.Close()

	dir := filepath.Dir(local)

	// A symlink to the film reads as the film itself.
	if err := os.Symlink("film.avi", filepath.Join(served, "link.avi")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, "link.avi")); err != nil || info.Size() != int64(len(data)) {
		t.Fatalf("Stat of a symlink through the mount = %v, %v; want size %d", info, err, len(data))
	}
	if got, err := os.ReadFile(filepath.Join(dir, "link.avi")); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("reading a symlink through the mount: err %v, equal %v", err, bytes.Equal(got, data))
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.tmp"), []byte("new text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(served, "notes.txt"), []byte("old, longer text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "notes.tmp"), filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(served, "notes.txt")); string(got) != "new text" {
		t.Fatalf("after save by rename, served file = %q", got)
	}
	if err := os.Truncate(filepath.Join(dir, "notes.txt"), 3); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(served, "notes.txt")); string(got) != "new" {
		t.Fatalf("after truncate, served file = %q", got)
	}
	if _, err := os.OpenFile(filepath.Join(dir, "notes.txt"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644); !errors.Is(err, os.ErrExist) {
		t.Fatalf("O_EXCL on an existing file = %v, want EEXIST", err)
	}

	// Timestamps and permissions go through to the server.
	when := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(dir, "notes.txt"), when, when); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(served, "notes.txt")); err != nil || !info.ModTime().Equal(when) {
		t.Fatalf("served mtime = %v, %v; want %v", info.ModTime(), err, when)
	}
	if err := os.Chmod(filepath.Join(dir, "notes.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(served, "notes.txt")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("served mode = %v, %v; want 0600", info.Mode(), err)
	}

	// statfs reports the served filesystem's real size.
	var viaMount, direct syscall.Statfs_t
	if err := syscall.Statfs(dir, &viaMount); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Statfs(served, &direct); err != nil {
		t.Fatal(err)
	}
	if got, want := viaMount.Blocks*uint64(viaMount.Bsize), direct.Blocks*uint64(direct.Frsize); got/4096 != want/4096 {
		t.Fatalf("statfs total through the mount = %d, want %d", got, want)
	}
}
