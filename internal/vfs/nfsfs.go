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
	"strings"

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
		isDir := it.IsDir()
		if isSymlink {
			if targetIsDir, err := n.symlinkTargetIsDir(path, it.Name()); err == nil {
				isDir = targetIsDir
			}
		}
		entries = append(entries, Entry{
			Name:      it.Name(),
			IsDir:     isDir,
			IsSymlink: isSymlink,
			Size:      it.Size(),
			Mode:      it.Mode(),
			ModTime:   it.ModTime(),
		})
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
	isDir := info.IsDir()
	if isSymlink {
		if targetIsDir, err := n.symlinkTargetIsDir(n.Dir(path), n.Base(path)); err == nil {
			isDir = targetIsDir
		}
	}
	return Entry{
		Name:      n.Base(path),
		IsDir:     isDir,
		IsSymlink: isSymlink,
		Size:      info.Size(),
		Mode:      info.Mode(),
		ModTime:   info.ModTime(),
	}, nil
}

// symlinkTargetIsDir follows the NFS symlink at dir/name and reports
// whether its target is a directory. Unlike SFTP/local Stat, NFSv3's LOOKUP
// never follows symlinks on its own (it returns the symlink's own NF3Lnk
// attributes) — resolving one needs an explicit READLINK for the target
// text, followed by a second LOOKUP on the resolved path.
func (n *NFSFS) symlinkTargetIsDir(dir, name string) (bool, error) {
	linkPath := n.Join(dir, name)
	f, err := n.target.Open(nfsPath(linkPath))
	if err != nil {
		return false, err
	}
	target, err := f.Readlink()
	if err != nil {
		return false, err
	}
	resolved := target
	if !strings.HasPrefix(target, "/") {
		resolved = n.Join(dir, target)
	}
	info, _, err := n.target.Lookup(nfsPath(resolved))
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
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

func (n *NFSFS) Rename(oldPath, newPath string) error {
	// go-nfs-client doesn't publicly expose NFSPROC3_RENAME: handled by
	// fileops via copy+delete.
	return ErrNotSupported
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
	return f, nil
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
