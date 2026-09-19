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
	"os"
	"strings"
	"time"

	"github.com/jfjallid/go-smb/smb"
	"github.com/jfjallid/go-smb/spnego"
)

// SMBOptions raggruppa i parametri necessari per collegarsi a una condivisione SMB.
type SMBOptions struct {
	Host     string
	Port     int // 0 -> 445
	Share    string
	Domain   string
	User     string
	Password string
	// Guest, if true, attempts an anonymous/guest login.
	Guest bool
}

// SMBFS exposes an SMB (CIFS) share through the common VFS interface,
// using exclusively the pure-Go library github.com/jfjallid/go-smb
// (no external command, no dependency on smbclient/cifs-utils).
type SMBFS struct {
	opts  SMBOptions
	conn  *smb.Connection
	label string
}

// DialSMB establishes the connection and authentication to the SMB server.
func DialSMB(opts SMBOptions) (*SMBFS, error) {
	if opts.Port == 0 {
		opts.Port = 445
	}
	initiator := &spnego.NTLMInitiator{
		User:     opts.User,
		Password: opts.Password,
		Domain:   opts.Domain,
	}
	if opts.Guest {
		initiator.User = ""
		initiator.Password = ""
	}
	conn, err := smb.NewConnection(smb.Options{
		Host:        opts.Host,
		Port:        opts.Port,
		Initiator:   initiator,
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("SMB connection to %s failed: %w", opts.Host, err)
	}
	if err := conn.TreeConnect(opts.Share); err != nil {
		conn.Close()
		return nil, fmt.Errorf("could not connect to share %q: %w", opts.Share, err)
	}
	label := fmt.Sprintf("smb://%s/%s", opts.Host, opts.Share)
	return &SMBFS{opts: opts, conn: conn, label: label}, nil
}

func (s *SMBFS) Kind() Kind    { return KindSMB }
func (s *SMBFS) Label() string { return s.label }
func (s *SMBFS) Root() string  { return "/" }

// smbPath converts a VFS path ("/a/b") into the format expected by go-smb
// (no leading slash, backslash separator).
func smbPath(p string) string {
	p = strings.TrimPrefix(p, "/")
	p = strings.ReplaceAll(p, "/", "\\")
	return p
}

func (s *SMBFS) List(path string) ([]Entry, error) {
	files, err := s.conn.ListDirectory(s.opts.Share, smbPath(path), "*")
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(files))
	for _, f := range files {
		name := f.Name
		if name == "." || name == ".." {
			continue
		}
		entries = append(entries, Entry{
			Name:      name,
			IsDir:     f.IsDir,
			IsSymlink: f.IsJunction,
			Size:      int64(f.Size),
			ModTime:   filetimeToTime(f.LastWriteTime),
		})
	}
	return entries, nil
}

func (s *SMBFS) Stat(path string) (Entry, error) {
	list, err := s.conn.ListDirectory(s.opts.Share, smbPath(s.Dir(path)), s.Base(path))
	if err != nil {
		return Entry{}, err
	}
	if len(list) == 0 {
		return Entry{}, os.ErrNotExist
	}
	f := list[0]
	return Entry{Name: f.Name, IsDir: f.IsDir, IsSymlink: f.IsJunction, Size: int64(f.Size), ModTime: filetimeToTime(f.LastWriteTime)}, nil
}

// filetimeToTime converts a Windows FILETIME timestamp (100ns intervals
// from 1601-01-01 UTC), as used by SMB, into a time.Time.
func filetimeToTime(ft uint64) time.Time {
	if ft == 0 {
		return time.Time{}
	}
	const epochDiff = 116444736000000000 // 1601-01-01 -> 1970-01-01 in 100ns units
	if ft < epochDiff {
		return time.Time{}
	}
	unixNano := (ft - epochDiff) * 100
	return time.Unix(0, int64(unixNano)).UTC()
}

func (s *SMBFS) Mkdir(path string) error { return s.conn.Mkdir(s.opts.Share, smbPath(path)) }

func (s *SMBFS) CreateEmptyFile(path string) error {
	w, err := s.Create(path)
	if err != nil {
		return err
	}
	return w.Close()
}

func (s *SMBFS) Remove(path string) error {
	entry, err := s.Stat(path)
	if err != nil {
		return err
	}
	if entry.IsDir {
		// go-smb requires the folder to be empty: empty it recursively first.
		children, err := s.List(path)
		if err != nil {
			return err
		}
		for _, c := range children {
			if err := s.Remove(s.Join(path, c.Name)); err != nil {
				return err
			}
		}
		return s.conn.DeleteDir(s.opts.Share, smbPath(path))
	}
	return s.conn.DeleteFile(s.opts.Share, smbPath(path))
}

func (s *SMBFS) Rename(oldPath, newPath string) error {
	// go-smb doesn't expose a direct rename in this public API: we implement
	// it via copy+delete on the caller's side (fileops), so here we signal
	// that it's not natively supported.
	return ErrNotSupported
}

type smbReadCloser struct {
	conn   *smb.Connection
	share  string
	path   string
	offset uint64
	closed bool
}

func (r *smbReadCloser) Read(p []byte) (int, error) {
	n := 0
	err := r.conn.RetrieveFile(r.share, r.path, r.offset, func(chunk []byte) (int, error) {
		c := copy(p[n:], chunk)
		n += c
		r.offset += uint64(c)
		if n >= len(p) {
			return c, io.EOF // stop this round of the download: further chunks are read via new calls
		}
		return len(chunk), nil
	})
	if n == 0 && err == nil {
		return 0, io.EOF
	}
	if err == io.EOF {
		err = nil
	}
	return n, err
}

func (r *smbReadCloser) Close() error { return nil }

func (s *SMBFS) Open(path string) (io.ReadCloser, error) {
	// Make sure the file exists before returning the reader.
	if _, err := s.Stat(path); err != nil {
		return nil, err
	}
	return &smbReadCloser{conn: s.conn, share: s.opts.Share, path: smbPath(path)}, nil
}

type smbWriteCloser struct {
	pw *io.PipeWriter
	wg chan error
}

func (w *smbWriteCloser) Write(p []byte) (int, error) { return w.pw.Write(p) }
func (w *smbWriteCloser) Close() error {
	err := w.pw.Close()
	putErr := <-w.wg
	if err == nil {
		err = putErr
	}
	return err
}

func (s *SMBFS) Create(path string) (io.WriteCloser, error) {
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := s.conn.PutFile(s.opts.Share, smbPath(path), 0, func(buf []byte) (int, error) {
			return pr.Read(buf)
		})
		pr.CloseWithError(err)
		done <- err
	}()
	return &smbWriteCloser{pw: pw, wg: done}, nil
}

func (s *SMBFS) Join(elem ...string) string {
	clean := make([]string, 0, len(elem))
	for _, e := range elem {
		e = strings.Trim(e, "/")
		if e != "" {
			clean = append(clean, e)
		}
	}
	return "/" + strings.Join(clean, "/")
}

func (s *SMBFS) Dir(path string) string {
	path = strings.TrimSuffix(path, "/")
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return "/"
	}
	return path[:idx]
}

func (s *SMBFS) Base(path string) string {
	path = strings.TrimSuffix(path, "/")
	idx := strings.LastIndex(path, "/")
	return path[idx+1:]
}

func (s *SMBFS) SupportsTrash() bool { return false }

func (s *SMBFS) Close() error {
	s.conn.Close()
	return nil
}
