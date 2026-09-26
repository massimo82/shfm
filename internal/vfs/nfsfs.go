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
	"sync"
	"time"

	nfsc "github.com/vmware/go-nfs-client/nfs"
	"github.com/vmware/go-nfs-client/nfs/rpc"
)

// NFSOptions groups the parameters needed to connect to an NFSv3 export.
type NFSOptions struct {
	Host   string // NFS server host or IP
	Export string // export path, e.g. "/srv/nfs/data"
	UID    uint32
	GID    uint32
}

// NFSFS exposes an NFSv3 export through the common VFS interface, using
// exclusively the pure-Go library github.com/vmware/go-nfs-client
// (no external mount.nfs).
type NFSFS struct {
	opts   NFSOptions
	mount  *nfsc.Mount
	target *nfsc.Target
	label  string
}

// DialNFS mounts (at the application level, without mount(8)) the given NFS export.
func DialNFS(opts NFSOptions) (*NFSFS, error) {
	mount, err := nfsc.DialMount(opts.Host)
	if err != nil {
		return nil, fmt.Errorf("connection to the mount daemon of %s failed: %w", opts.Host, err)
	}
	auth := rpc.NewAuthUnix("shfm", opts.UID, opts.GID)
	target, err := mount.Mount(opts.Export, auth.Auth())
	if err != nil {
		mount.Unmount()
		return nil, fmt.Errorf("mounting export %q failed: %w", opts.Export, err)
	}
	label := fmt.Sprintf("nfs://%s%s", opts.Host, opts.Export)
	return &NFSFS{opts: opts, mount: mount, target: target, label: label}, nil
}

func (n *NFSFS) Kind() Kind    { return KindNFS }
func (n *NFSFS) Label() string { return n.label }
func (n *NFSFS) Root() string  { return "/" }

func nfsPath(p string) string {
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "."
	}
	return p
}

func (n *NFSFS) List(path string) ([]Entry, error) {
	items, err := n.target.ReadDirPlus(nfsPath(path))
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(items))
	for _, it := range items {
		if it.Name() == "." || it.Name() == ".." {
			continue
		}
		isSymlink := it.Attr.IsSet && it.Attr.Attr.Type == nfsc.NF3Lnk
		e := Entry{
			Name:      it.Name(),
			IsDir:     it.IsDir(),
			IsSymlink: isSymlink,
			Size:      it.Size(),
			Mode:      it.Mode(),
			ModTime:   it.ModTime(),
		}
		if isSymlink {
			if target, err := n.symlinkTarget(path, it.Name()); err == nil {
				followSymlink(&e, target)
			}
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (n *NFSFS) Stat(path string) (Entry, error) {
	info, _, err := n.target.Lookup(nfsPath(path))
	if err != nil {
		return Entry{}, err
	}
	fattr, _ := info.(*nfsc.Fattr)
	isSymlink := fattr != nil && fattr.Type == nfsc.NF3Lnk
	e := Entry{
		Name:      n.Base(path),
		IsDir:     info.IsDir(),
		IsSymlink: isSymlink,
		Size:      info.Size(),
		Mode:      info.Mode(),
		ModTime:   info.ModTime(),
	}
	if isSymlink {
		if target, err := n.symlinkTarget(n.Dir(path), n.Base(path)); err == nil {
			followSymlink(&e, target)
		}
	}
	return e, nil
}

// symlinkTargetIsDir follows the NFS symlink at dir/name and reports
// whether its target is a directory. Unlike SFTP/local Stat, NFSv3's LOOKUP
// never follows symlinks on its own (it returns the symlink's own NF3Lnk
// attributes) — resolving one needs an explicit READLINK for the target
// text, followed by a second LOOKUP on the resolved path.
// symlinkTarget returns the attributes of the target of the symlink name
// in dir.
func (n *NFSFS) symlinkTarget(dir, name string) (os.FileInfo, error) {
	linkPath := n.Join(dir, name)
	f, err := n.target.Open(nfsPath(linkPath))
	if err != nil {
		return nil, err
	}
	target, err := f.Readlink()
	if err != nil {
		return nil, err
	}
	resolved := target
	if !strings.HasPrefix(target, "/") {
		resolved = n.Join(dir, target)
	}
	info, _, err := n.target.Lookup(nfsPath(resolved))
	if err != nil {
		return nil, err
	}
	return info, nil
}

func (n *NFSFS) Mkdir(path string) error {
	_, err := n.target.Mkdir(nfsPath(path), 0o755)
	return err
}

func (n *NFSFS) CreateEmptyFile(path string) error {
	_, err := n.target.Create(nfsPath(path), 0o644)
	return err
}

func (n *NFSFS) Remove(path string) error {
	entry, err := n.Stat(path)
	if err != nil {
		return err
	}
	if entry.IsDir {
		return n.target.RemoveAll(nfsPath(path))
	}
	return n.target.Remove(nfsPath(path))
}

// Rename renames/moves natively on the server (NFSPROC3_RENAME, patched
// into third_party/go-nfs-client), replacing an existing destination.
func (n *NFSFS) Rename(oldPath, newPath string) error {
	return n.target.Rename(nfsPath(oldPath), nfsPath(newPath))
}

func (n *NFSFS) Open(path string) (io.ReadCloser, error) {
	f, err := n.target.Open(nfsPath(path))
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (n *NFSFS) Create(path string) (io.WriteCloser, error) {
	f, err := n.target.OpenFile(nfsPath(path), 0o644)
	if err != nil {
		return nil, err
	}
	// OpenFile doesn't truncate an existing file: without this, overwriting
	// a longer file would leave its old tail after the new content.
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// nfsRandomFile adapts go-nfs-client's File, which reads and writes at a
// current position, to RandomAccessFile; mu keeps a seek and the
// read/write that follows it together.
type nfsRandomFile struct {
	mu sync.Mutex
	f  *nfsc.File
}

func (r *nfsRandomFile) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.f.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	n := 0
	for n < len(p) {
		// Read returns at most one server READ's worth per call.
		c, err := r.f.Read(p[n:])
		n += c
		if err != nil {
			return n, err
		}
		if c == 0 {
			return n, io.EOF
		}
	}
	return n, nil
}

func (r *nfsRandomFile) WriteAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.f.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	return r.f.Write(p)
}

func (r *nfsRandomFile) Truncate(size int64) error { return r.f.Truncate(uint64(size)) }

func (r *nfsRandomFile) Close() error { return r.f.Close() }

// OpenRandom implements RandomAccessOpener.
func (n *NFSFS) OpenRandom(path string, flag int, perm os.FileMode) (RandomAccessFile, error) {
	p := nfsPath(path)
	var f *nfsc.File
	var err error
	switch {
	case flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		if _, _, lerr := n.target.Lookup(p); lerr == nil {
			return nil, os.ErrExist
		}
		if _, err = n.target.Create(p, perm); err == nil {
			f, err = n.target.Open(p)
		}
	case flag&os.O_CREATE != 0:
		f, err = n.target.OpenFile(p, perm)
	default:
		f, err = n.target.Open(p)
	}
	if err != nil {
		return nil, err
	}
	if flag&os.O_TRUNC != 0 && flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		if err := f.Truncate(0); err != nil {
			return nil, err
		}
	}
	return &nfsRandomFile{f: f}, nil
}

// Redial implements Redialer.
func (n *NFSFS) Redial() (FileSystem, error) { return DialNFS(n.opts) }

// Chmod implements PermissionsEditor (NFSPROC3_SETATTR).
func (n *NFSFS) Chmod(path string, mode os.FileMode) error {
	return n.target.SetAttr(nfsPath(path), nfsc.Sattr3{Mode: nfsc.SetMode{SetIt: true, Mode: uint32(mode.Perm())}})
}

// Chown implements PermissionsEditor; a negative uid or gid is left
// unchanged. The server applies its own rules (root_squash, ...).
func (n *NFSFS) Chown(path string, uid, gid int) error {
	var attr nfsc.Sattr3
	if uid >= 0 {
		attr.UID = nfsc.SetUID{SetIt: true, UID: uint32(uid)}
	}
	if gid >= 0 {
		attr.GID = nfsc.SetUID{SetIt: true, UID: uint32(gid)}
	}
	return n.target.SetAttr(nfsPath(path), attr)
}

// Chtimes implements TimesSetter.
func (n *NFSFS) Chtimes(path string, atime, mtime time.Time) error {
	var attr nfsc.Sattr3
	if !atime.IsZero() {
		attr.Atime = nfsc.ClientTime(atime)
	}
	if !mtime.IsZero() {
		attr.Mtime = nfsc.ClientTime(mtime)
	}
	return n.target.SetAttr(nfsPath(path), attr)
}

// Space implements SpaceReporter (NFSPROC3_FSSTAT).
func (n *NFSFS) Space(path string) (total, free uint64, err error) {
	return n.target.FSStat()
}

func (n *NFSFS) Join(elem ...string) string {
	clean := make([]string, 0, len(elem))
	for _, e := range elem {
		e = strings.Trim(e, "/")
		if e != "" {
			clean = append(clean, e)
		}
	}
	return "/" + strings.Join(clean, "/")
}

func (n *NFSFS) Dir(path string) string {
	path = strings.TrimSuffix(path, "/")
	idx := strings.LastIndex(path, "/")
	if idx <= 0 {
		return "/"
	}
	return path[:idx]
}

func (n *NFSFS) Base(path string) string {
	path = strings.TrimSuffix(path, "/")
	idx := strings.LastIndex(path, "/")
	return path[idx+1:]
}

func (n *NFSFS) SupportsTrash() bool { return false }

func (n *NFSFS) Close() error {
	_ = n.mount.Unmount()
	return nil
}
