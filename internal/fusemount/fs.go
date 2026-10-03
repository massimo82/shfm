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
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"shfm/internal/vfs"
)

// reconnectInterval is the minimum time between two reconnection attempts,
// so that a server that's really gone doesn't get a new connection attempt
// for every single request an application makes.
const reconnectInterval = 5 * time.Second

// mountFS is the state shared by every node of one mount: the backend
// connection, owned by the mount alone (never the UI's), and what's needed
// to replace it when it drops.
type mountFS struct {
	// mu serializes every backend call. Not every backend client is safe
	// for concurrent use (go-nfs-client isn't), while the kernel sends
	// requests concurrently.
	mu        sync.Mutex
	be        vfs.FileSystem
	ra        vfs.RandomAccessOpener
	gen       uint64 // bumped on every reconnection, see handle.gen
	lastDial  time.Time
	closed    bool
	openFiles atomic.Int64

	// shared is set when be is the UI's own connection (a vfs.SingleSession
	// backend) rather than one opened for the mount; ownsBackend says
	// whether closing the mount must close be too — always for a redialed
	// connection, and for a shared one once the UI has let go of it (see
	// Manager.Release).
	shared      bool
	ownsBackend atomic.Bool

	uid, gid uint32
}

func newMountFS(be vfs.FileSystem, shared bool) (*mountFS, error) {
	ra, ok := be.(vfs.RandomAccessOpener)
	if !ok {
		return nil, vfs.ErrNotSupported
	}
	m := &mountFS{be: be, ra: ra, shared: shared, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}
	m.ownsBackend.Store(!shared)
	return m, nil
}

// call runs op against the backend. If op fails in a way that may mean the
// connection is gone, it reconnects and runs op once more.
func (m *mountFS) call(op func(be vfs.FileSystem, ra vfs.RandomAccessOpener) error) syscall.Errno {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return syscall.ENOTCONN
	}
	err := op(m.be, m.ra)
	if vfs.IsConnectionFailure(err) && m.reconnectLocked() {
		err = op(m.be, m.ra)
	}
	return toErrno(err)
}

// reconnectLocked replaces the backend connection with a new one; m.mu
// must be held. Handles opened on the old connection notice the new gen
// and reopen their file on the new one.
func (m *mountFS) reconnectLocked() bool {
	rd, ok := m.be.(vfs.Redialer)
	if !ok || time.Since(m.lastDial) < reconnectInterval {
		return false
	}
	m.lastDial = time.Now()
	be, err := rd.Redial()
	if err != nil {
		return false
	}
	ra, ok := be.(vfs.RandomAccessOpener)
	if !ok {
		be.Close()
		return false
	}
	old := m.be
	m.be, m.ra = be, ra
	m.gen++
	go old.Close()
	return true
}

// close ends the mount's use of the backend — closing the connection too
// if the mount owns it; every later request fails.
func (m *mountFS) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		m.closed = true
		if m.ownsBackend.Load() {
			m.be.Close()
		}
	}
}

// toErrno converts a backend error into the errno reported to the
// application.
func toErrno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return syscall.ENOENT
	case errors.Is(err, os.ErrExist):
		return syscall.EEXIST
	case errors.Is(err, os.ErrPermission):
		return syscall.EACCES
	case errors.Is(err, vfs.ErrNotSupported):
		return syscall.ENOTSUP
	case errors.Is(err, os.ErrInvalid):
		return syscall.EINVAL
	}
	return syscall.EIO
}

// fillAttr converts a backend entry into FUSE attributes. Every entry is
// reported as owned by the local user, as gvfs does: access control is up
// to the server, which checks the credentials the connection was opened
// with.
func (m *mountFS) fillAttr(e vfs.Entry, out *fuse.Attr) {
	perm := uint32(e.Mode.Perm())
	if e.IsDir {
		if perm == 0 {
			perm = 0o755
		}
		out.Mode = syscall.S_IFDIR | perm
	} else {
		if perm == 0 {
			perm = 0o644
		}
		out.Mode = syscall.S_IFREG | perm
		out.Size = uint64(e.Size)
		out.Blocks = (out.Size + 511) / 512
	}
	out.Nlink = 1
	out.Blksize = 128 * 1024
	out.Owner = fuse.Owner{Uid: m.uid, Gid: m.gid}
	t := e.ModTime
	if t.IsZero() {
		t = time.Unix(0, 0)
	}
	out.SetTimes(&t, &t, &t)
}

func typeOf(e vfs.Entry) uint32 {
	if e.IsDir {
		return syscall.S_IFDIR
	}
	return syscall.S_IFREG
}

// node is a file or directory of the mount. It keeps no backend state of
// its own: its backend path is derived from its position in the tree, so
// renames need no bookkeeping here.
type node struct {
	fs.Inode
	m *mountFS

	// sizeUnknown is the entry's vfs.Entry.SizeUnknown as last seen: such
	// a file is opened in direct I/O mode, so the kernel reads it up to
	// EOF instead of stopping at its reported (zero) size.
	sizeUnknown atomic.Bool
}

var (
	_ fs.NodeLookuper  = (*node)(nil)
	_ fs.NodeGetattrer = (*node)(nil)
	_ fs.NodeSetattrer = (*node)(nil)
	_ fs.NodeReaddirer = (*node)(nil)
	_ fs.NodeOpener    = (*node)(nil)
	_ fs.NodeCreater   = (*node)(nil)
	_ fs.NodeMkdirer   = (*node)(nil)
	_ fs.NodeUnlinker  = (*node)(nil)
	_ fs.NodeRmdirer   = (*node)(nil)
	_ fs.NodeRenamer   = (*node)(nil)
	_ fs.NodeStatfser  = (*node)(nil)
)

// path returns the node's backend path.
func (n *node) path() string {
	return "/" + n.Path(nil)
}

func (n *node) childPath(name string) string {
	if p := n.path(); p != "/" {
		return p + "/" + name
	}
	return "/" + name
}

func (n *node) stat(p string) (vfs.Entry, syscall.Errno) {
	if p == "/" {
		// The root of a share/export always exists and is a folder; not
		// every backend can stat it (SMB stats by listing the parent).
		return vfs.Entry{IsDir: true}, 0
	}
	var e vfs.Entry
	errno := n.m.call(func(be vfs.FileSystem, _ vfs.RandomAccessOpener) error {
		var err error
		e, err = be.Stat(p)
		return err
	})
	return e, errno
}

func (n *node) newChild(ctx context.Context, e vfs.Entry, out *fuse.EntryOut) (*node, *fs.Inode) {
	child := &node{m: n.m}
	n.m.fillAttr(e, &out.Attr)
	child.sizeUnknown.Store(e.SizeUnknown)
	return child, n.NewInode(ctx, child, fs.StableAttr{Mode: typeOf(e)})
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	e, errno := n.stat(n.childPath(name))
	if errno != 0 {
		return nil, errno
	}
	_, inode := n.newChild(ctx, e, out)
	return inode, 0
}

func (n *node) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	e, errno := n.stat(n.path())
	if errno != 0 {
		return errno
	}
	n.sizeUnknown.Store(e.SizeUnknown)
	n.m.fillAttr(e, &out.Attr)
	return 0
}

// Setattr supports changing the size (truncate), and — where the backend
// can — the mode (vfs.PermissionsEditor), the owner and the timestamps
// (vfs.TimesSetter). A change the backend can't make is accepted and
// ignored, as gvfs does, so that tools like cp -p don't fail outright; so
// is a change of owner to the local user, whom every entry is reported as
// owned by already.
func (n *node) Setattr(ctx context.Context, fh fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	p := n.path()
	if size, ok := in.GetSize(); ok {
		var errno syscall.Errno
		if h, ok := fh.(*handle); ok && h.writable() {
			errno = h.do(func(f vfs.RandomAccessFile) error { return f.Truncate(int64(size)) })
		} else {
			errno = n.m.call(func(_ vfs.FileSystem, ra vfs.RandomAccessOpener) error {
				f, err := ra.OpenRandom(p, os.O_WRONLY, 0)
				if err != nil {
					return err
				}
				err = f.Truncate(int64(size))
				if cerr := f.Close(); err == nil {
					err = cerr
				}
				return err
			})
		}
		if errno != 0 {
			return errno
		}
	}
	if mode, ok := in.GetMode(); ok {
		errno := n.m.call(func(be vfs.FileSystem, _ vfs.RandomAccessOpener) error {
			if pe, ok := be.(vfs.PermissionsEditor); ok {
				return pe.Chmod(p, os.FileMode(mode&0o777))
			}
			return nil
		})
		if errno != 0 {
			return errno
		}
	}
	uid, uidSet := in.GetUID()
	gid, gidSet := in.GetGID()
	uidSet = uidSet && uid != n.m.uid
	gidSet = gidSet && gid != n.m.gid
	if uidSet || gidSet {
		newUID, newGID := -1, -1
		if uidSet {
			newUID = int(uid)
		}
		if gidSet {
			newGID = int(gid)
		}
		errno := n.m.call(func(be vfs.FileSystem, _ vfs.RandomAccessOpener) error {
			if pe, ok := be.(vfs.PermissionsEditor); ok {
				return pe.Chown(p, newUID, newGID)
			}
			return nil
		})
		if errno != 0 {
			return errno
		}
	}
	atime, atimeSet := in.GetATime()
	mtime, mtimeSet := in.GetMTime()
	if atimeSet || mtimeSet {
		errno := n.m.call(func(be vfs.FileSystem, _ vfs.RandomAccessOpener) error {
			if ts, ok := be.(vfs.TimesSetter); ok {
				return ts.Chtimes(p, atime, mtime) // unset ones are zero: unchanged
			}
			return nil
		})
		if errno != 0 {
			return errno
		}
	}
	return n.Getattr(ctx, fh, out)
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	var entries []vfs.Entry
	p := n.path()
	errno := n.m.call(func(be vfs.FileSystem, _ vfs.RandomAccessOpener) error {
		var err error
		entries, err = be.List(p)
		return err
	})
	if errno != 0 {
		return nil, errno
	}
	list := make([]fuse.DirEntry, 0, len(entries))
	for _, e := range entries {
		list = append(list, fuse.DirEntry{Name: e.Name, Mode: typeOf(e)})
	}
	return fs.NewListDirStream(list), 0
}

// openFlags keeps the open(2) flags OpenRandom understands.
func openFlags(flags uint32) int {
	return int(flags) & (syscall.O_ACCMODE | os.O_CREATE | os.O_EXCL | os.O_TRUNC)
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	h, errno := n.openHandle(n.path(), openFlags(flags), 0)
	if errno != 0 {
		return nil, 0, errno
	}
	if n.sizeUnknown.Load() {
		return h, fuse.FOPEN_DIRECT_IO, 0
	}
	return h, 0, 0
}

func (n *node) openHandle(p string, flag int, perm os.FileMode) (*handle, syscall.Errno) {
	h := &handle{node: n, flag: flag &^ (os.O_CREATE | os.O_EXCL | os.O_TRUNC)}
	errno := n.m.call(func(_ vfs.FileSystem, ra vfs.RandomAccessOpener) error {
		f, err := ra.OpenRandom(p, flag, perm)
		if err != nil {
			return err
		}
		h.f, h.gen = f, n.m.gen
		return nil
	})
	if errno != 0 {
		return nil, errno
	}
	n.m.openFiles.Add(1)
	return h, 0
}

func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	p := n.childPath(name)
	child := &node{m: n.m}
	flag := openFlags(flags) | os.O_CREATE
	perm := os.FileMode(mode & 0o777)
	// The child isn't in the tree yet, so open by the explicit path.
	h, errno := child.openHandle(p, flag, perm)
	if errno != 0 {
		return nil, nil, 0, errno
	}
	e, errno := n.stat(p)
	if errno != 0 {
		e = vfs.Entry{Name: name, Mode: perm, ModTime: time.Now()}
	}
	n.m.fillAttr(e, &out.Attr)
	inode := n.NewInode(ctx, child, fs.StableAttr{Mode: syscall.S_IFREG})
	return inode, h, 0, 0
}

func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	p := n.childPath(name)
	errno := n.m.call(func(be vfs.FileSystem, _ vfs.RandomAccessOpener) error { return be.Mkdir(p) })
	if errno != 0 {
		return nil, errno
	}
	e, errno := n.stat(p)
	if errno != 0 {
		e = vfs.Entry{Name: name, IsDir: true, ModTime: time.Now()}
	}
	_, inode := n.newChild(ctx, e, out)
	return inode, 0
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	p := n.childPath(name)
	return n.m.call(func(be vfs.FileSystem, _ vfs.RandomAccessOpener) error {
		// vfs.FileSystem.Remove deletes folders recursively: make sure
		// unlink(2) can never do that.
		e, err := be.Stat(p)
		if err != nil {
			return err
		}
		if e.IsDir {
			return syscall.EISDIR
		}
		return be.Remove(p)
	})
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	p := n.childPath(name)
	return n.m.call(func(be vfs.FileSystem, _ vfs.RandomAccessOpener) error {
		// Same as Unlink: rmdir(2) must fail on a non-empty folder rather
		// than delete its contents.
		e, err := be.Stat(p)
		if err != nil {
			return err
		}
		if !e.IsDir {
			return syscall.ENOTDIR
		}
		children, err := be.List(p)
		if err != nil {
			return err
		}
		if len(children) > 0 {
			return syscall.ENOTEMPTY
		}
		return be.Remove(p)
	})
}

const renameNoReplace = 0x1 // RENAME_NOREPLACE, renameat2(2)

func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if flags&fs.RENAME_EXCHANGE != 0 {
		return syscall.EINVAL
	}
	np, ok := newParent.(*node)
	if !ok {
		return syscall.EXDEV
	}
	from, to := n.childPath(name), np.childPath(newName)
	return n.m.call(func(be vfs.FileSystem, _ vfs.RandomAccessOpener) error {
		if flags&renameNoReplace != 0 {
			if _, err := be.Stat(to); err == nil {
				return syscall.EEXIST
			}
		}
		err := be.Rename(from, to)
		if !errors.Is(err, vfs.ErrNotSupported) {
			return err
		}
		// The backend can't rename this natively (an MTP device that can't
		// rename at all, or across folders): copy and delete, for a file.
		// A folder gets EXDEV, which makes mv(1) copy it itself.
		e, err := be.Stat(from)
		if err != nil {
			return err
		}
		if e.IsDir {
			return syscall.EXDEV
		}
		return copyAndDelete(be, from, to)
	})
}

// copyAndDelete moves the file from to to, within be, by copying its
// content and then deleting from; an existing destination file is
// replaced, as rename(2) does. The content goes through a local temp file:
// a backend may not allow reading one file while writing another (MTP
// holds its single session for the whole of a download).
func copyAndDelete(be vfs.FileSystem, from, to string) error {
	tmp, err := os.CreateTemp("", "shfm-fuse-move-*")
	if err != nil {
		return err
	}
	os.Remove(tmp.Name())
	defer tmp.Close()

	src, err := be.Open(from)
	if err != nil {
		return err
	}
	size, err := io.Copy(tmp, src)
	if cerr := src.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}

	if e, err := be.Stat(to); err == nil {
		if e.IsDir {
			return syscall.EISDIR
		}
		if err := be.Remove(to); err != nil {
			return err
		}
	}
	var dst io.WriteCloser
	if sc, ok := be.(vfs.SizedCreator); ok {
		dst, err = sc.CreateSized(to, size)
	} else {
		dst, err = be.Create(to)
	}
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, tmp); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return be.Remove(from)
}

// Statfs reports the source's real capacity where the backend can tell
// (vfs.SpaceReporter); otherwise a large, unknown one, since a zero-size
// filesystem would make applications refuse to write to it.
func (n *node) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	const bsize = 4096
	total, free := uint64(1<<32)*bsize, uint64(1<<32)*bsize
	p := n.path()
	n.m.call(func(be vfs.FileSystem, _ vfs.RandomAccessOpener) error {
		if sr, ok := be.(vfs.SpaceReporter); ok {
			if t, f, err := sr.Space(p); err == nil && t > 0 {
				total, free = t, f
			}
		}
		return nil
	})
	out.Bsize = bsize
	out.Frsize = bsize
	out.Blocks = total / bsize
	out.Bfree = free / bsize
	out.Bavail = free / bsize
	out.Files, out.Ffree = 1<<20, 1<<20
	out.NameLen = 255
	return 0
}

// handle is an open file. It remembers how it was opened so that, after a
// reconnection, it can reopen the file on the new connection.
type handle struct {
	node *node
	flag int // open flags, minus O_CREATE/O_EXCL/O_TRUNC
	f    vfs.RandomAccessFile
	gen  uint64 // the mountFS.gen f was opened on
}

var (
	_ fs.FileReader   = (*handle)(nil)
	_ fs.FileWriter   = (*handle)(nil)
	_ fs.FileReleaser = (*handle)(nil)
	_ fs.FileFlusher  = (*handle)(nil)
	_ fs.FileFsyncer  = (*handle)(nil)
)

func (h *handle) writable() bool { return h.flag&(os.O_WRONLY|os.O_RDWR) != 0 }

// do runs op on the open file, reopening it first if the connection it
// was opened on has since been replaced, and once more after a
// reconnection if op fails in a way that may mean the connection is gone.
func (h *handle) do(op func(f vfs.RandomAccessFile) error) syscall.Errno {
	m := h.node.m
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return syscall.ENOTCONN
	}
	if h.gen != m.gen {
		if err := h.reopenLocked(); err != nil {
			return toErrno(err)
		}
	}
	err := op(h.f)
	if err != io.EOF && vfs.IsConnectionFailure(err) && m.reconnectLocked() {
		if err = h.reopenLocked(); err == nil {
			err = op(h.f)
		}
	}
	return toErrno(err)
}

func (h *handle) reopenLocked() error {
	m := h.node.m
	f, err := m.ra.OpenRandom(h.node.path(), h.flag, 0)
	if err != nil {
		return err
	}
	h.f, h.gen = f, m.gen
	return nil
}

func (h *handle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	var n int
	errno := h.do(func(f vfs.RandomAccessFile) error {
		var err error
		n, err = f.ReadAt(dest, off)
		if errors.Is(err, io.EOF) {
			return nil // a short read at the end of the file
		}
		return err
	})
	if errno != 0 {
		return nil, errno
	}
	return fuse.ReadResultData(dest[:n]), 0
}

func (h *handle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	var n int
	errno := h.do(func(f vfs.RandomAccessFile) error {
		var err error
		n, err = f.WriteAt(data, off)
		return err
	})
	if errno != 0 {
		return 0, errno
	}
	return uint32(n), 0
}

// Flush and Fsync have nothing to do: every write has already been sent to
// the server by the time it returns.
func (h *handle) Flush(ctx context.Context) syscall.Errno               { return 0 }
func (h *handle) Fsync(ctx context.Context, flags uint32) syscall.Errno { return 0 }

func (h *handle) Release(ctx context.Context) syscall.Errno {
	m := h.node.m
	defer m.openFiles.Add(-1)
	m.mu.Lock()
	defer m.mu.Unlock()
	if h.gen != m.gen || m.closed {
		return 0 // opened on a connection that's already gone
	}
	return toErrno(h.f.Close())
}
