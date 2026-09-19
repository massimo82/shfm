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
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SFTPOptions groups the parameters needed to connect to an SFTP server.
type SFTPOptions struct {
	Host       string
	Port       int // 0 -> 22
	User       string
	Password   string // empty to try key-based auth instead
	PrivateKey string // path to a private key file; empty tries ~/.ssh/id_ed25519 and ~/.ssh/id_rsa
	BasePath   string // initial directory, empty -> server default ("home" via ".")
}

// SFTPFS exposes an SFTP server through the common VFS interface, using
// golang.org/x/crypto/ssh (SSH transport/auth) and github.com/pkg/sftp (the
// SFTP protocol on top) — both established, widely used pure-Go libraries,
// no external ssh/sftp/scp commands involved.
type SFTPFS struct {
	sshConn *ssh.Client
	client  *sftp.Client
	label   string
	root    string
}

// DialSFTP connects and authenticates to the SFTP server described by opts.
//
// Host key verification follows a trust-on-first-use (TOFU) policy against
// the user's ~/.ssh/known_hosts, exactly like OpenSSH the first time you
// connect interactively: an unknown host is accepted and its key is
// recorded; a host whose key has since *changed* is refused, protecting
// against man-in-the-middle attacks on subsequent connections.
func DialSFTP(opts SFTPOptions) (*SFTPFS, error) {
	if opts.Port == 0 {
		opts.Port = 22
	}
	auths, err := sftpAuthMethods(opts)
	if err != nil {
		return nil, err
	}

	hostKeyCallback, err := tofuHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("could not prepare host key verification: %w", err)
	}

	cfg := &ssh.ClientConfig{
		User:            opts.User,
		Auth:            auths,
		HostKeyCallback: hostKeyCallback,
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port))
	sshConn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("SSH connection to %s failed: %w", addr, err)
	}
	client, err := sftp.NewClient(sshConn)
	if err != nil {
		sshConn.Close()
		return nil, fmt.Errorf("SFTP session on %s failed: %w", addr, err)
	}

	root := opts.BasePath
	if root == "" {
		if wd, err := client.Getwd(); err == nil {
			root = wd
		} else {
			root = "/"
		}
	}

	label := fmt.Sprintf("sftp://%s@%s", opts.User, opts.Host)
	return &SFTPFS{sshConn: sshConn, client: client, label: label, root: root}, nil
}

func sftpAuthMethods(opts SFTPOptions) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if opts.Password != "" {
		methods = append(methods, ssh.Password(opts.Password))
	}
	keyPaths := []string{}
	if opts.PrivateKey != "" {
		keyPaths = append(keyPaths, opts.PrivateKey)
	} else if home, err := os.UserHomeDir(); err == nil {
		keyPaths = append(keyPaths, home+"/.ssh/id_ed25519", home+"/.ssh/id_rsa")
	}
	for _, kp := range keyPaths {
		data, err := os.ReadFile(kp)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			continue // unreadable/passphrase-protected key: skip rather than fail the whole connection
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("no authentication method available: provide a password or a usable private key")
	}
	return methods, nil
}

// tofuHostKeyCallback builds a ssh.HostKeyCallback backed by
// ~/.ssh/known_hosts, creating the file if missing, and automatically
// trusting-and-recording keys for hosts seen for the first time (the
// "trust on first use" model).
func tofuHostKeyCallback() (ssh.HostKeyCallback, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := home + "/.ssh"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := dir + "/known_hosts"
	if _, err := os.OpenFile(path, os.O_CREATE, 0o600); err == nil {
		// ensure the file exists, ignore an already-open error race
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if !isKnownHostsKeyError(err, &keyErr) {
			return err
		}
		if len(keyErr.Want) > 0 {
			// The host IS known, but under a *different* key: refuse, this
			// is exactly the case TOFU exists to catch.
			return fmt.Errorf("host key for %s changed since the last connection — refusing (possible man-in-the-middle); "+
				"remove the stale entry from %s if you're sure this is expected", hostname, path)
		}
		// Unknown host: trust it and record it for next time.
		return appendKnownHost(path, hostname, key)
	}, nil
}

func isKnownHostsKeyError(err error, out **knownhosts.KeyError) bool {
	ke, ok := err.(*knownhosts.KeyError)
	if ok {
		*out = ke
	}
	return ok
}

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line := knownhosts.Line([]string{hostname}, key)
	_, err = f.WriteString(line + "\n")
	return err
}

func (s *SFTPFS) Kind() Kind    { return KindSFTP }
func (s *SFTPFS) Label() string { return s.label }
func (s *SFTPFS) Root() string  { return s.root }

func (s *SFTPFS) List(p string) ([]Entry, error) {
	infos, err := s.client.ReadDir(p)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(infos))
	for _, info := range infos {
		e := entryFromInfo(info)
		if e.IsSymlink {
			// ReadDir, like Lstat, reports the symlink's own type: resolve
			// the target so a symlink to a folder can actually be navigated
			// into, matching Stat()'s behavior for the same entry.
			if target, err := s.client.Stat(path.Join(p, e.Name)); err == nil {
				e.IsDir = target.IsDir()
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (s *SFTPFS) Stat(p string) (Entry, error) {
	info, err := s.client.Lstat(p)
	if err != nil {
		return Entry{}, err
	}
	e := entryFromInfo(info)
	if e.IsSymlink {
		if target, err := s.client.Stat(p); err == nil {
			e.IsDir = target.IsDir()
		}
	}
	return e, nil
}

func entryFromInfo(info os.FileInfo) Entry {
	mode := info.Mode()
	return Entry{
		Name:      info.Name(),
		IsDir:     info.IsDir(),
		IsSymlink: mode&os.ModeSymlink != 0,
		Size:      info.Size(),
		Mode:      mode,
		ModTime:   info.ModTime(),
	}
}

func (s *SFTPFS) Mkdir(p string) error { return s.client.Mkdir(p) }
func (s *SFTPFS) CreateEmptyFile(p string) error {
	f, err := s.client.Create(p)
	if err != nil {
		return err
	}
	return f.Close()
}

func (s *SFTPFS) Remove(p string) error {
	info, err := s.client.Lstat(p)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return s.client.Remove(p)
	}
	children, err := s.client.ReadDir(p)
	if err != nil {
		return err
	}
	for _, c := range children {
		if err := s.Remove(path.Join(p, c.Name())); err != nil {
			return err
		}
	}
	return s.client.RemoveDirectory(p)
}

func (s *SFTPFS) Rename(oldPath, newPath string) error {
	// PosixRename overwrites the destination atomically when the server
	// supports the "posix-rename@openssh.com" extension (virtually every
	// modern OpenSSH server does); fall back to the plain SFTP Rename
	// (which fails if the destination exists) otherwise.
	if err := s.client.PosixRename(oldPath, newPath); err == nil {
		return nil
	}
	return s.client.Rename(oldPath, newPath)
}

func (s *SFTPFS) Open(p string) (io.ReadCloser, error)    { return s.client.Open(p) }
func (s *SFTPFS) Create(p string) (io.WriteCloser, error) { return s.client.Create(p) }

func (s *SFTPFS) Join(elem ...string) string { return path.Join(elem...) }
func (s *SFTPFS) Dir(p string) string        { return path.Dir(p) }
func (s *SFTPFS) Base(p string) string       { return path.Base(p) }
func (s *SFTPFS) SupportsTrash() bool        { return false }

func (s *SFTPFS) Close() error {
	s.client.Close()
	return s.sshConn.Close()
}

// Chmod implements vfs.PermissionsEditor.
func (s *SFTPFS) Chmod(p string, mode os.FileMode) error { return s.client.Chmod(p, mode) }

// Chown implements vfs.PermissionsEditor.
func (s *SFTPFS) Chown(p string, uid, gid int) error { return s.client.Chown(p, uid, gid) }
