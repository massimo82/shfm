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

//go:build vault

package vault

import (
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"syscall"
	"time"

	"shfm/internal/vfs"
)

// layout maps the vault's paths ("/a/b", always clean) to files and
// folders on the backend: indexLayout for encrypted names, plainLayout for
// plain ones. The content of files is the FS's business.
type layout interface {
	list(dir string) ([]vfs.Entry, error)
	stat(p string) (vfs.Entry, error)
	mkdir(p string) error
	remove(p string) error
	rename(oldPath, newPath string) error
	// blob returns the backend path of file p's age content.
	blob(p string) (string, error)
	// beginWrite returns where to write file p's new age content; commit
	// makes it p's content (size: its plaintext bytes), abort drops it.
	beginWrite(p string) (blob string, commit func(size int64) error, abort func(), err error)
	chtimes(p string, mtime time.Time) error
}

// vaultSignature maps each of the two files that make a folder a vault
// (vault.json, identity.age) to the other: a vault folder may hold one, not
// both (ErrVaultInVault).
var vaultSignature = map[string]string{configFile: identityFile, identityFile: configFile}

// maxNameBytes is the longest name most backends accept.
const maxNameBytes = 255

func validName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsRune(name, '/') {
		return syscall.EINVAL
	}
	return nil
}

func dirEntry(name string, mtime time.Time) vfs.Entry {
	return vfs.Entry{Name: name, IsDir: true, Mode: os.ModeDir | 0o755, ModTime: mtime}
}

func fileEntry(name string, size int64, mtime time.Time) vfs.Entry {
	return vfs.Entry{Name: name, Size: size, Mode: 0o644, ModTime: mtime}
}

func pathErr(op, p string, err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) || errors.Is(err, ErrLocked) {
		return err
	}
	return &os.PathError{Op: op, Path: p, Err: err}
}

// ---- encrypted names ----

type indexLayout struct {
	st *state
	fs vfs.FileSystem
}

// resolveDir returns the backend folder of the vault folder p. s.mu held.
func (l *indexLayout) resolveDir(p string) (string, error) {
	cur := l.st.dir
	if p == "/" {
		return cur, nil
	}
	for _, name := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		x, err := l.st.loadIndex(l.fs, cur, false)
		if err != nil {
			return "", err
		}
		e, ok := x.get(name)
		if !ok {
			// Maybe made through another connection since it was cached.
			if x, err = l.st.loadIndex(l.fs, cur, true); err != nil {
				return "", err
			}
			if e, ok = x.get(name); !ok {
				return "", errNotExist("open", p)
			}
		}
		if !e.Dir {
			return "", pathErr("open", p, syscall.ENOTDIR)
		}
		cur = l.fs.Join(cur, e.ID)
	}
	return cur, nil
}

// lookup returns p's parent backend folder, its index and p's entry. s.mu
// held.
func (l *indexLayout) lookup(p string, fresh bool) (string, *index, entry, bool, error) {
	pdir, err := l.resolveDir(path.Dir(p))
	if err != nil {
		return "", nil, entry{}, false, err
	}
	x, err := l.st.loadIndex(l.fs, pdir, fresh)
	if err != nil {
		return "", nil, entry{}, false, err
	}
	e, ok := x.get(path.Base(p))
	if !ok && !fresh {
		if x, err = l.st.loadIndex(l.fs, pdir, true); err != nil {
			return "", nil, entry{}, false, err
		}
		e, ok = x.get(path.Base(p))
	}
	return pdir, x, e, ok, nil
}

func (e entry) toVFS() vfs.Entry {
	mtime := time.Unix(0, e.MTime)
	if e.Dir {
		return dirEntry(e.Name, mtime)
	}
	return fileEntry(e.Name, e.Size, mtime)
}

func (l *indexLayout) list(dir string) ([]vfs.Entry, error) {
	l.st.mu.Lock()
	defer l.st.mu.Unlock()
	bdir, err := l.resolveDir(dir)
	if err != nil {
		return nil, err
	}
	x, err := l.st.loadIndex(l.fs, bdir, true)
	if err != nil {
		return nil, err
	}
	out := make([]vfs.Entry, 0, len(x.Entries))
	for _, e := range x.Entries {
		out = append(out, e.toVFS())
	}
	return out, nil
}

func (l *indexLayout) stat(p string) (vfs.Entry, error) {
	l.st.mu.Lock()
	defer l.st.mu.Unlock()
	if p == "/" {
		if l.st.identity == nil {
			return vfs.Entry{}, ErrLocked
		}
		return dirEntry("/", time.Time{}), nil
	}
	_, _, e, ok, err := l.lookup(p, false)
	if err != nil {
		return vfs.Entry{}, err
	}
	if !ok {
		return vfs.Entry{}, errNotExist("stat", p)
	}
	return e.toVFS(), nil
}

func (l *indexLayout) mkdir(p string) error {
	if err := validName(path.Base(p)); err != nil {
		return pathErr("mkdir", p, err)
	}
	l.st.mu.Lock()
	defer l.st.mu.Unlock()
	pdir, x, _, ok, err := l.lookup(p, true)
	if err != nil {
		return err
	}
	if ok {
		return pathErr("mkdir", p, os.ErrExist)
	}
	e := entry{ID: newID(), Name: path.Base(p), Dir: true, MTime: time.Now().UnixNano()}
	bdir := l.fs.Join(pdir, e.ID)
	if err := l.fs.Mkdir(bdir); err != nil {
		return err
	}
	empty, err := l.st.sealIndex(&index{})
	if err == nil {
		err = writeSmall(l.fs, l.fs.Join(bdir, indexFile), empty)
	}
	if err == nil {
		x = x.clone()
		x.put(e)
		err = l.st.saveIndex(l.fs, pdir, x)
	}
	if err != nil {
		l.fs.Remove(bdir)
		return err
	}
	l.st.idx[bdir] = &index{byName: map[string]int{}}
	return nil
}

// forget drops the cached indexes of the backend folder bdir and below.
// s.mu held.
func (l *indexLayout) forget(bdir string) {
	for k := range l.st.idx {
		if k == bdir || strings.HasPrefix(k, bdir+"/") {
			delete(l.st.idx, k)
		}
	}
}

func (l *indexLayout) remove(p string) error {
	if p == "/" {
		return pathErr("remove", p, syscall.EINVAL)
	}
	l.st.mu.Lock()
	defer l.st.mu.Unlock()
	pdir, x, e, ok, err := l.lookup(p, true)
	if err != nil {
		return err
	}
	if !ok {
		return errNotExist("remove", p)
	}
	x = x.clone()
	x.remove(e.Name)
	if err := l.st.saveIndex(l.fs, pdir, x); err != nil {
		return err
	}
	b := l.fs.Join(pdir, e.blob())
	if e.Dir {
		l.forget(b)
	}
	// Out of the index, the entry is gone for the vault: what's left on
	// the backend if this fails is an orphan nothing refers to.
	return removeIfExists(l.fs, b)
}

func (l *indexLayout) rename(oldPath, newPath string) error {
	if oldPath == "/" || newPath == "/" {
		return pathErr("rename", oldPath, syscall.EINVAL)
	}
	if err := validName(path.Base(newPath)); err != nil {
		return pathErr("rename", newPath, err)
	}
	if oldPath == newPath {
		return nil
	}
	if strings.HasPrefix(newPath, oldPath+"/") {
		return pathErr("rename", oldPath, syscall.EINVAL) // into itself
	}
	l.st.mu.Lock()
	defer l.st.mu.Unlock()
	opdir, ox, e, ok, err := l.lookup(oldPath, true)
	if err != nil {
		return err
	}
	if !ok {
		return errNotExist("rename", oldPath)
	}
	npdir, nx, target, exists, err := l.lookup(newPath, true)
	if err != nil {
		return err
	}
	if exists && (target.Dir || e.Dir) {
		return pathErr("rename", newPath, os.ErrExist)
	}
	if other, sig := vaultSignature[path.Base(newPath)]; sig && !e.Dir {
		if o, has := nx.get(other); has && !o.Dir && !(opdir == npdir && o.Name == e.Name) {
			return pathErr("rename", newPath, ErrVaultInVault)
		}
	}
	moved := e
	moved.Name = path.Base(newPath)
	if opdir == npdir {
		x := ox.clone()
		x.remove(e.Name)
		x.put(moved)
		if err := l.st.saveIndex(l.fs, opdir, x); err != nil {
			return err
		}
	} else {
		from, to := l.fs.Join(opdir, e.blob()), l.fs.Join(npdir, e.blob())
		if err := l.fs.Rename(from, to); err != nil {
			return err // vfs.ErrNotSupported: the caller copies instead
		}
		x := nx.clone()
		x.put(moved)
		if err := l.st.saveIndex(l.fs, npdir, x); err != nil {
			l.fs.Rename(to, from)
			return err
		}
		x = ox.clone()
		x.remove(e.Name)
		if err := l.st.saveIndex(l.fs, opdir, x); err != nil {
			return err
		}
		if e.Dir {
			l.forget(from)
		}
	}
	if exists {
		removeIfExists(l.fs, l.fs.Join(npdir, target.blob()))
	}
	return nil
}

func (l *indexLayout) blob(p string) (string, error) {
	l.st.mu.Lock()
	defer l.st.mu.Unlock()
	pdir, _, e, ok, err := l.lookup(p, false)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errNotExist("open", p)
	}
	if e.Dir {
		return "", pathErr("open", p, syscall.EISDIR)
	}
	return l.fs.Join(pdir, e.blob()), nil
}

func (l *indexLayout) beginWrite(p string) (string, func(int64) error, func(), error) {
	name := path.Base(p)
	if err := validName(name); err != nil {
		return "", nil, nil, pathErr("create", p, err)
	}
	l.st.mu.Lock()
	pdir, x, e, ok, err := l.lookup(p, false)
	l.st.mu.Unlock()
	if err != nil {
		return "", nil, nil, err
	}
	if ok && e.Dir {
		return "", nil, nil, pathErr("create", p, syscall.EISDIR)
	}
	if other, sig := vaultSignature[name]; sig {
		if o, has := x.get(other); has && !o.Dir {
			return "", nil, nil, pathErr("create", p, ErrVaultInVault)
		}
	}
	id := newID()
	b := l.fs.Join(pdir, id+".age")
	abort := func() { removeIfExists(l.fs, b) }
	commit := func(size int64) error {
		l.st.mu.Lock()
		defer l.st.mu.Unlock()
		x, err := l.st.loadIndex(l.fs, pdir, true)
		if err != nil {
			return err
		}
		old, had := x.get(name)
		if had && old.Dir {
			return pathErr("create", p, syscall.EISDIR)
		}
		x = x.clone()
		x.put(entry{ID: id, Name: name, Size: size, MTime: time.Now().UnixNano()})
		if err := l.st.saveIndex(l.fs, pdir, x); err != nil {
			return err
		}
		if had {
			removeIfExists(l.fs, l.fs.Join(pdir, old.blob()))
		}
		return nil
	}
	return b, commit, abort, nil
}

func (l *indexLayout) chtimes(p string, mtime time.Time) error {
	if mtime.IsZero() || p == "/" {
		return nil
	}
	l.st.mu.Lock()
	defer l.st.mu.Unlock()
	pdir, x, e, ok, err := l.lookup(p, true)
	if err != nil {
		return err
	}
	if !ok {
		return errNotExist("chtimes", p)
	}
	e.MTime = mtime.UnixNano()
	x = x.clone()
	x.put(e)
	return l.st.saveIndex(l.fs, pdir, x)
}

// ---- plain names ----

type plainLayout struct {
	st *state
	fs vfs.FileSystem
}

// tempPrefix starts the names of files being written, hidden from the
// listing until complete.
const tempPrefix = ".shfm-"

// reservedRoot are the names, in the vault's root folder, of its own files.
var reservedRoot = map[string]bool{
	configFile: true, identityFile: true, identityFile + tmpSuffix: true, identityFile + bakSuffix: true,
	recoveryFile: true,
}

func (l *plainLayout) dirPath(p string) string {
	if p == "/" {
		return l.st.dir
	}
	return l.fs.Join(append([]string{l.st.dir}, strings.Split(strings.TrimPrefix(p, "/"), "/")...)...)
}

func (l *plainLayout) blobPath(p string) string { return l.dirPath(p) + ".age" }

// check refuses a name the vault can't store at p.
func (l *plainLayout) check(op, p string, dir bool) error {
	name := path.Base(p)
	if err := validName(name); err != nil {
		return pathErr(op, p, err)
	}
	stored := name
	if !dir {
		stored += ".age"
	}
	if len(stored) > maxNameBytes {
		return pathErr(op, p, ErrNameTooLong)
	}
	if strings.HasPrefix(name, tempPrefix) || (path.Dir(p) == "/" && reservedRoot[stored]) {
		return pathErr(op, p, ErrReservedName)
	}
	return nil
}

func (l *plainLayout) live() error {
	if l.st.locked.Load() {
		return ErrLocked
	}
	return nil
}

func (l *plainLayout) list(dir string) ([]vfs.Entry, error) {
	if err := l.live(); err != nil {
		return nil, err
	}
	des, err := l.fs.List(l.dirPath(dir))
	if err != nil {
		return nil, err
	}
	out := make([]vfs.Entry, 0, len(des))
	for _, de := range des {
		if strings.HasPrefix(de.Name, tempPrefix) || (dir == "/" && reservedRoot[de.Name]) {
			continue
		}
		if de.IsDir {
			out = append(out, dirEntry(de.Name, de.ModTime))
			continue
		}
		name, ok := strings.CutSuffix(de.Name, ".age")
		if !ok || name == "" {
			continue // not vault content
		}
		e := fileEntry(name, 0, de.ModTime)
		e.Size, ok = plainSize(l.st.overhead, de.Size)
		e.SizeUnknown = !ok
		out = append(out, e)
	}
	return out, nil
}

func (l *plainLayout) stat(p string) (vfs.Entry, error) {
	if err := l.live(); err != nil {
		return vfs.Entry{}, err
	}
	if p == "/" {
		return dirEntry("/", time.Time{}), nil
	}
	if l.check("stat", p, true) != nil && l.check("stat", p, false) != nil {
		return vfs.Entry{}, errNotExist("stat", p)
	}
	if de, err := l.fs.Stat(l.blobPath(p)); err == nil && !de.IsDir {
		e := fileEntry(path.Base(p), 0, de.ModTime)
		var ok bool
		e.Size, ok = plainSize(l.st.overhead, de.Size)
		e.SizeUnknown = !ok
		return e, nil
	}
	if de, err := l.fs.Stat(l.dirPath(p)); err == nil && de.IsDir {
		return dirEntry(path.Base(p), de.ModTime), nil
	}
	return vfs.Entry{}, errNotExist("stat", p)
}

func (l *plainLayout) mkdir(p string) error {
	if err := l.live(); err != nil {
		return err
	}
	if err := l.check("mkdir", p, true); err != nil {
		return err
	}
	if exists(l.fs, l.blobPath(p)) {
		return pathErr("mkdir", p, os.ErrExist)
	}
	return l.fs.Mkdir(l.dirPath(p))
}

func (l *plainLayout) remove(p string) error {
	if err := l.live(); err != nil {
		return err
	}
	if p == "/" {
		return pathErr("remove", p, syscall.EINVAL)
	}
	if exists(l.fs, l.blobPath(p)) {
		return l.fs.Remove(l.blobPath(p))
	}
	if de, err := l.fs.Stat(l.dirPath(p)); err == nil && de.IsDir && l.check("remove", p, true) == nil {
		return l.fs.Remove(l.dirPath(p))
	}
	return errNotExist("remove", p)
}

func (l *plainLayout) rename(oldPath, newPath string) error {
	if err := l.live(); err != nil {
		return err
	}
	if oldPath == "/" || newPath == "/" || strings.HasPrefix(newPath, oldPath+"/") {
		return pathErr("rename", oldPath, syscall.EINVAL)
	}
	if oldPath == newPath {
		return nil
	}
	if exists(l.fs, l.blobPath(oldPath)) {
		if err := l.check("rename", newPath, false); err != nil {
			return err
		}
		if de, err := l.fs.Stat(l.dirPath(newPath)); err == nil && de.IsDir {
			return pathErr("rename", newPath, os.ErrExist)
		}
		if l.signatureClash(newPath) && path.Join(path.Dir(newPath), vaultSignature[path.Base(newPath)]) != oldPath {
			return pathErr("rename", newPath, ErrVaultInVault)
		}
		if err := removeIfExists(l.fs, l.blobPath(newPath)); err != nil {
			return err
		}
		return l.fs.Rename(l.blobPath(oldPath), l.blobPath(newPath))
	}
	if de, err := l.fs.Stat(l.dirPath(oldPath)); err != nil || !de.IsDir {
		return errNotExist("rename", oldPath)
	}
	if err := l.check("rename", newPath, true); err != nil {
		return err
	}
	if exists(l.fs, l.blobPath(newPath)) || exists(l.fs, l.dirPath(newPath)) {
		return pathErr("rename", newPath, os.ErrExist)
	}
	return l.fs.Rename(l.dirPath(oldPath), l.dirPath(newPath))
}

func (l *plainLayout) blob(p string) (string, error) {
	if err := l.live(); err != nil {
		return "", err
	}
	if p == "/" || l.check("open", p, false) != nil {
		return "", errNotExist("open", p)
	}
	return l.blobPath(p), nil
}

func (l *plainLayout) beginWrite(p string) (string, func(int64) error, func(), error) {
	if err := l.live(); err != nil {
		return "", nil, nil, err
	}
	if err := l.check("create", p, false); err != nil {
		return "", nil, nil, err
	}
	if de, err := l.fs.Stat(l.dirPath(p)); err == nil && de.IsDir {
		return "", nil, nil, pathErr("create", p, syscall.EISDIR)
	}
	if l.signatureClash(p) {
		return "", nil, nil, pathErr("create", p, ErrVaultInVault)
	}
	tmp := l.fs.Join(l.dirPath(path.Dir(p)), tempPrefix+newID()+".part")
	final := l.blobPath(p)
	abort := func() { removeIfExists(l.fs, tmp) }
	commit := func(int64) error {
		if err := removeIfExists(l.fs, final); err != nil {
			return err
		}
		return moveFile(l.fs, tmp, final)
	}
	return tmp, commit, abort, nil
}

// signatureClash reports whether writing file p would give its folder
// both of a vault's signature files.
func (l *plainLayout) signatureClash(p string) bool {
	other, sig := vaultSignature[path.Base(p)]
	if !sig {
		return false
	}
	de, err := l.fs.Stat(l.blobPath(path.Join(path.Dir(p), other)))
	return err == nil && !de.IsDir
}

// moveFile moves a file of any size to to, which doesn't exist: renamed,
// or copied and removed where the backend can't rename.
func moveFile(fs vfs.FileSystem, from, to string) error {
	err := fs.Rename(from, to)
	if !errors.Is(err, vfs.ErrNotSupported) {
		return err
	}
	src, err := fs.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	var dst io.WriteCloser
	if sc, ok := fs.(vfs.SizedCreator); ok {
		var e vfs.Entry
		if e, err = fs.Stat(from); err == nil {
			dst, err = sc.CreateSized(to, e.Size)
		}
	} else {
		dst, err = fs.Create(to)
	}
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		removeIfExists(fs, to)
		return err
	}
	if err := dst.Close(); err != nil {
		removeIfExists(fs, to)
		return err
	}
	src.Close()
	return fs.Remove(from)
}

func (l *plainLayout) chtimes(p string, mtime time.Time) error {
	if err := l.live(); err != nil {
		return err
	}
	ts, ok := l.fs.(vfs.TimesSetter)
	if !ok {
		return vfs.ErrNotSupported
	}
	if exists(l.fs, l.blobPath(p)) {
		return ts.Chtimes(l.blobPath(p), time.Time{}, mtime)
	}
	return ts.Chtimes(l.dirPath(p), time.Time{}, mtime)
}
