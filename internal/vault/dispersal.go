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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"shfm/internal/vfs"
)

// A split vault is a vault stored on a DispersedFS: three folders on three
// sources (three cloud accounts, a NAS and a cloud…), none of which holds
// a whole file. Every file is stored as three shards:
//
//	A = the first half (the larger, for an odd size), on part 1
//	B = the second half, on part 2
//	C = A XOR B (B padded with a zero byte to A's length), on part 3
//
// Any two shards give the file back — A and B simply put together, or the
// missing half as the XOR of the other two — so one source may be lost,
// or unreachable, without losing anything; and no source alone has a
// file, not even its encrypted form (in a vault, the files are age files
// before being split: a shard is half of an encrypted file).
//
// Each shard is named "<name>.<gen>.a", ".b", or ".c0"/".c1" (the padding,
// 0 or 1 byte, in C's name), where gen identifies one write of the file:
// a reader only puts together shards of the same generation, so a write
// interrupted part-way (one source updated, not the others) never mixes
// two versions. Folders are plain folders, the same on the three parts.
//
// Reading needs two parts; writing needs all three — with one missing the
// vault is read-only, until the part is back or replaced (RepairDispersed
// rebuilds an empty replacement from the other two).

// DispersedFS stores files split over three Parts, see above. Safe for
// concurrent use as far as the parts' backends are.
type DispersedFS struct {
	label string
	parts [3]Part

	mu    sync.Mutex
	cache map[string]*listing // by dispersed folder path
}

var (
	_ vfs.FileSystem         = (*DispersedFS)(nil)
	_ vfs.RandomAccessOpener = (*DispersedFS)(nil)
	_ vfs.Redialer           = (*DispersedFS)(nil)
	_ vfs.SizedCreator       = (*DispersedFS)(nil)
)

// NewDispersed returns the file system over parts, named label. It owns
// the parts' backends: Close closes them.
func NewDispersed(label string, parts [3]Part) *DispersedFS {
	return &DispersedFS{label: label, parts: parts, cache: map[string]*listing{}}
}

// Split is NewDispersed as a plain vfs.FileSystem, for callers that must
// also build without the vault module.
func Split(label string, parts [3]Part) vfs.FileSystem { return NewDispersed(label, parts) }

// IsSplit reports whether fs is a split vault's DispersedFS.
func IsSplit(fs vfs.FileSystem) bool {
	_, ok := fs.(*DispersedFS)
	return ok
}

// UnavailableParts lists the parts (0-2) of split vault storage fs whose
// source isn't connected.
func UnavailableParts(fs vfs.FileSystem) []int {
	if d, ok := fs.(*DispersedFS); ok {
		return d.Unavailable()
	}
	return nil
}

// Parts returns the three parts.
func (d *DispersedFS) Parts() [3]Part { return d.parts }

// Unavailable lists the parts (0-2) whose source isn't connected, or
// whose folder doesn't answer (one round trip to each source).
func (d *DispersedFS) Unavailable() []int {
	var out []int
	for i, p := range d.parts {
		if p.FS == nil {
			out = append(out, i)
		} else if e, err := p.FS.Stat(p.Dir); err != nil || !e.IsDir {
			out = append(out, i)
		}
	}
	return out
}

func (d *DispersedFS) Kind() vfs.Kind { return vfs.KindDispersed }
func (d *DispersedFS) Label() string  { return d.label }
func (d *DispersedFS) Root() string   { return "/" }

func (d *DispersedFS) Join(elem ...string) string { return path.Join(elem...) }
func (d *DispersedFS) Dir(p string) string        { return path.Dir(p) }
func (d *DispersedFS) Base(p string) string       { return path.Base(p) }
func (d *DispersedFS) SupportsTrash() bool        { return false }

// Close closes the three parts' backends.
func (d *DispersedFS) Close() error {
	var first error
	for _, p := range d.parts {
		if p.FS != nil {
			if err := p.FS.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

// Redial implements vfs.Redialer: new connections to the three sources (a
// local part, which has none, is reused). A part that can't be redialed,
// or isn't connected, makes it fail.
func (d *DispersedFS) Redial() (vfs.FileSystem, error) {
	var parts [3]Part
	for i, p := range d.parts {
		parts[i].Dir = p.Dir
		switch fs := p.FS.(type) {
		case nil:
			return nil, ErrPartUnavailable
		case vfs.Redialer:
			nfs, err := fs.Redial()
			if err != nil {
				for _, q := range parts[:i] {
					if _, local := q.FS.(vfs.LocalPath); !local {
						q.FS.Close()
					}
				}
				return nil, err
			}
			parts[i].FS = nfs
		case vfs.LocalPath:
			parts[i].FS = p.FS
		default:
			return nil, vfs.ErrNotSupported
		}
	}
	return NewDispersed(d.label, parts), nil
}

// partPath is dispersed path p (clean, "/"-rooted) on part i.
func (d *DispersedFS) partPath(i int, p string) string {
	pp := d.parts[i]
	if p == "/" {
		return pp.Dir
	}
	return pp.FS.Join(append([]string{pp.Dir}, strings.Split(strings.TrimPrefix(p, "/"), "/")...)...)
}

// all fails unless every part's source is connected (whether each folder
// answers, the change itself finds out).
func (d *DispersedFS) all() error {
	for _, p := range d.parts {
		if p.FS == nil {
			return ErrPartUnavailable
		}
	}
	return nil
}

// ---- shard names ----

var shardRE = regexp.MustCompile(`^(.+)\.([0-9a-f]{8})\.(a|b|c0|c1)$`)

const shardTemp = ".shfm-split-"

func shardName(name, gen string, part, pad int) string {
	switch part {
	case 0:
		return name + "." + gen + ".a"
	case 1:
		return name + "." + gen + ".b"
	}
	return fmt.Sprintf("%s.%s.c%d", name, gen, pad)
}

func newGen() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// generation is one write of a file: its shards found on the parts.
type generation struct {
	gen   string
	have  [3]bool
	size  [3]int64
	pad   int
	mtime time.Time
}

func (g *generation) count() int {
	n := 0
	for _, h := range g.have {
		if h {
			n++
		}
	}
	return n
}

// consistent reports whether the shards' sizes fit together.
func (g *generation) consistent() bool {
	a, b, c := g.size[0], g.size[1], g.size[2]
	switch {
	case g.have[0] && g.have[2] && a != c:
		return false
	case g.have[1] && g.have[2] && b != c-int64(g.pad):
		return false
	case g.have[0] && g.have[1] && (a-b < 0 || a-b > 1):
		return false
	}
	return true
}

// fileSize is the whole file's size.
func (g *generation) fileSize() int64 {
	switch {
	case g.have[0] && g.have[1]:
		return g.size[0] + g.size[1]
	case g.have[0]:
		return 2*g.size[0] - int64(g.pad)
	default:
		return 2*g.size[2] - int64(g.pad)
	}
}

// halves returns the sizes of A and B.
func (g *generation) halves() (int64, int64) {
	n := g.fileSize()
	a := (n + 1) / 2
	return a, n - a
}

type splitFile struct {
	gens map[string]*generation
	best *generation // nil: no generation readable
}

// listing is a dispersed folder, merged from its parts.
type listing struct {
	at    time.Time
	ok    int            // parts listed
	dirs  map[string]int // folder name → parts having it
	dtime map[string]time.Time
	files map[string]*splitFile
	// names on each part, to remove every shard of a file
	raw [3][]string
}

// listingTTL is how long a folder's merged listing is reused: the
// vault reads the same folder several times per operation.
const listingTTL = 2 * time.Second

func (d *DispersedFS) invalidate(dirs ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, dir := range dirs {
		delete(d.cache, dir)
	}
}

// list merges dispersed folder dir from its parts: at least two must list
// it.
func (d *DispersedFS) list(dir string, fresh bool) (*listing, error) {
	d.mu.Lock()
	if l, ok := d.cache[dir]; ok && !fresh && time.Since(l.at) < listingTTL {
		d.mu.Unlock()
		return l, nil
	}
	d.mu.Unlock()

	var res [3][]vfs.Entry
	var errs [3]error
	var wg sync.WaitGroup
	for i := range d.parts {
		if d.parts[i].FS == nil {
			errs[i] = ErrPartUnavailable
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i], errs[i] = d.parts[i].FS.List(d.partPath(i, dir))
		}()
	}
	wg.Wait()

	l := &listing{at: time.Now(), dirs: map[string]int{}, dtime: map[string]time.Time{}, files: map[string]*splitFile{}}
	var firstErr error
	for i := range d.parts {
		if errs[i] != nil {
			if firstErr == nil || errors.Is(firstErr, ErrPartUnavailable) {
				firstErr = errs[i]
			}
			continue
		}
		l.ok++
		for _, e := range res[i] {
			l.raw[i] = append(l.raw[i], e.Name)
			if e.IsDir {
				l.dirs[e.Name]++
				if e.ModTime.After(l.dtime[e.Name]) {
					l.dtime[e.Name] = e.ModTime
				}
				continue
			}
			m := shardRE.FindStringSubmatch(e.Name)
			if m == nil {
				continue // a temp file, or something else
			}
			name, gen, kind := m[1], m[2], m[3]
			part := map[string]int{"a": 0, "b": 1, "c0": 2, "c1": 2}[kind]
			if part != i {
				continue // a shard on the wrong part isn't one of ours
			}
			fe := l.files[name]
			if fe == nil {
				fe = &splitFile{gens: map[string]*generation{}}
				l.files[name] = fe
			}
			g := fe.gens[gen]
			if g == nil {
				g = &generation{gen: gen}
				fe.gens[gen] = g
			}
			g.have[i], g.size[i] = true, e.Size
			if part == 2 && kind == "c1" {
				g.pad = 1
			}
			if e.ModTime.After(g.mtime) {
				g.mtime = e.ModTime
			}
		}
	}
	if l.ok < 2 {
		return nil, firstErr
	}
	for _, fe := range l.files {
		for _, g := range fe.gens {
			if g.count() < 2 || !g.consistent() {
				continue
			}
			if b := fe.best; b == nil || g.count() > b.count() || (g.count() == b.count() && g.mtime.After(b.mtime)) {
				fe.best = g
			}
		}
	}
	d.mu.Lock()
	d.cache[dir] = l
	d.mu.Unlock()
	return l, nil
}

// lookup finds p (not the root) in its folder's listing.
func (d *DispersedFS) lookup(p string, fresh bool) (*listing, *generation, bool, error) {
	l, err := d.list(path.Dir(p), fresh)
	if err != nil {
		return nil, nil, false, err
	}
	name := path.Base(p)
	if l.dirs[name] >= 2 {
		return l, nil, true, nil
	}
	if fe := l.files[name]; fe != nil && fe.best != nil {
		return l, fe.best, false, nil
	}
	return l, nil, false, nil
}

func (d *DispersedFS) List(p string) ([]vfs.Entry, error) {
	p = clean(p)
	l, err := d.list(p, true)
	if err != nil {
		return nil, err
	}
	var out []vfs.Entry
	for name, n := range l.dirs {
		if n >= 2 {
			out = append(out, dirEntry(name, l.dtime[name]))
		}
	}
	for name, fe := range l.files {
		if fe.best != nil && l.dirs[name] < 2 {
			out = append(out, fileEntry(name, fe.best.fileSize(), fe.best.mtime))
		}
	}
	return out, nil
}

func (d *DispersedFS) Stat(p string) (vfs.Entry, error) {
	p = clean(p)
	if p == "/" {
		if _, err := d.list("/", false); err != nil {
			return vfs.Entry{}, err
		}
		return dirEntry("/", time.Time{}), nil
	}
	l, g, isDir, err := d.lookup(p, false)
	if err != nil {
		return vfs.Entry{}, err
	}
	switch {
	case isDir:
		return dirEntry(path.Base(p), l.dtime[path.Base(p)]), nil
	case g != nil:
		return fileEntry(path.Base(p), g.fileSize(), g.mtime), nil
	}
	return vfs.Entry{}, errNotExist("stat", p)
}

func (d *DispersedFS) Mkdir(p string) error {
	p = clean(p)
	if err := d.all(); err != nil {
		return err
	}
	_, g, isDir, err := d.lookup(p, true)
	if err != nil {
		return err
	}
	if isDir || g != nil {
		return pathErr("mkdir", p, os.ErrExist)
	}
	var made []int
	for i := range d.parts {
		pp := d.partPath(i, p)
		if e, err := d.parts[i].FS.Stat(pp); err == nil && e.IsDir {
			continue // left over on this part
		}
		if err := d.parts[i].FS.Mkdir(pp); err != nil {
			for _, j := range made {
				d.parts[j].FS.Remove(d.partPath(j, p))
			}
			return err
		}
		made = append(made, i)
	}
	d.invalidate(path.Dir(p))
	return nil
}

func (d *DispersedFS) CreateEmptyFile(p string) error {
	p = clean(p)
	if _, err := d.Stat(p); err == nil {
		return pathErr("create", p, os.ErrExist)
	}
	w, err := d.Create(p)
	if err != nil {
		return err
	}
	return w.Close()
}

// removeShards removes every shard of file name in folder dir, of every
// generation but keep's, on every part.
func (d *DispersedFS) removeShards(l *listing, dir, name, keep string) error {
	var first error
	for i := range d.parts {
		if d.parts[i].FS == nil {
			continue
		}
		for _, raw := range l.raw[i] {
			m := shardRE.FindStringSubmatch(raw)
			if m == nil || m[1] != name || m[2] == keep {
				continue
			}
			if err := d.parts[i].FS.Remove(d.parts[i].FS.Join(d.partPath(i, dir), raw)); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}

func (d *DispersedFS) Remove(p string) error {
	p = clean(p)
	if p == "/" {
		return pathErr("remove", p, syscall.EINVAL)
	}
	if err := d.all(); err != nil {
		return err
	}
	l, g, isDir, err := d.lookup(p, true)
	if err != nil {
		return err
	}
	dir := path.Dir(p)
	defer d.invalidate(dir, p)
	if isDir {
		var first error
		for i := range d.parts {
			if err := removeIfExists(d.parts[i].FS, d.partPath(i, p)); err != nil && first == nil {
				first = err
			}
		}
		d.invalidatePrefix(p)
		return first
	}
	if g == nil {
		return errNotExist("remove", p)
	}
	return d.removeShards(l, dir, path.Base(p), "")
}

func (d *DispersedFS) invalidatePrefix(p string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k := range d.cache {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(d.cache, k)
		}
	}
}

// Rename renames or moves a file or folder on every part: vfs.ErrNotSupported
// when the backends can't (nothing changed then).
func (d *DispersedFS) Rename(oldPath, newPath string) error {
	oldPath, newPath = clean(oldPath), clean(newPath)
	if oldPath == "/" || newPath == "/" || strings.HasPrefix(newPath, oldPath+"/") {
		return pathErr("rename", oldPath, syscall.EINVAL)
	}
	if oldPath == newPath {
		return nil
	}
	if err := d.all(); err != nil {
		return err
	}
	ol, og, oDir, err := d.lookup(oldPath, true)
	if err != nil {
		return err
	}
	if !oDir && og == nil {
		return errNotExist("rename", oldPath)
	}
	nl, ng, nDir, err := d.lookup(newPath, true)
	if err != nil {
		return err
	}
	if nDir || (ng != nil && oDir) {
		return pathErr("rename", newPath, os.ErrExist)
	}
	oldDir, newDir := path.Dir(oldPath), path.Dir(newPath)
	defer d.invalidate(oldDir, newDir)
	if oDir {
		for i := range d.parts {
			err := d.parts[i].FS.Rename(d.partPath(i, oldPath), d.partPath(i, newPath))
			if err != nil {
				for j := range i { // undo, to leave the parts alike
					d.parts[j].FS.Rename(d.partPath(j, newPath), d.partPath(j, oldPath))
				}
				return err
			}
		}
		d.invalidatePrefix(oldPath)
		return nil
	}
	if ng != nil {
		if err := d.removeShards(nl, newDir, path.Base(newPath), ""); err != nil {
			return err
		}
	}
	oldName, newName := path.Base(oldPath), path.Base(newPath)
	for i := range d.parts {
		if !og.have[i] {
			continue
		}
		from := d.parts[i].FS.Join(d.partPath(i, oldDir), shardName(oldName, og.gen, i, og.pad))
		to := d.parts[i].FS.Join(d.partPath(i, newDir), shardName(newName, og.gen, i, og.pad))
		if err := d.parts[i].FS.Rename(from, to); err != nil {
			for j := range i {
				if og.have[j] {
					d.parts[j].FS.Rename(d.parts[j].FS.Join(d.partPath(j, newDir), shardName(newName, og.gen, j, og.pad)),
						d.parts[j].FS.Join(d.partPath(j, oldDir), shardName(oldName, og.gen, j, og.pad)))
				}
			}
			return err
		}
	}
	d.removeShards(ol, oldDir, oldName, og.gen)
	return nil
}

// ---- reading ----

// shardReaderAt opens shard i of g in folder dir for random access.
func (d *DispersedFS) shardReaderAt(i int, dir, name string, g *generation) (io.ReaderAt, io.Closer, error) {
	pp := d.parts[i].FS.Join(d.partPath(i, dir), shardName(name, g.gen, i, g.pad))
	if ra, ok := d.parts[i].FS.(vfs.RandomAccessOpener); ok {
		f, err := ra.OpenRandom(pp, os.O_RDONLY, 0)
		if err != nil {
			return nil, nil, err
		}
		return f, f, nil
	}
	r, err := d.parts[i].FS.Open(pp)
	if err != nil {
		return nil, nil, err
	}
	at, ok := r.(io.ReaderAt)
	if !ok {
		r.Close()
		return nil, nil, vfs.ErrNotSupported
	}
	return at, r, nil
}

func (d *DispersedFS) openShard(i int, dir, name string, g *generation) (io.ReadCloser, error) {
	return d.parts[i].FS.Open(d.parts[i].FS.Join(d.partPath(i, dir), shardName(name, g.gen, i, g.pad)))
}

// Open reads file p from two of its shards: A then B, or a missing half
// rebuilt as the XOR of the other two.
func (d *DispersedFS) Open(p string) (io.ReadCloser, error) {
	p = clean(p)
	_, g, isDir, err := d.lookup(p, false)
	if err != nil {
		return nil, err
	}
	if isDir {
		return nil, pathErr("open", p, syscall.EISDIR)
	}
	if g == nil {
		return nil, errNotExist("open", p)
	}
	dir, name := path.Dir(p), path.Base(p)
	lenA, lenB := g.halves()
	open := func(i int) (io.ReadCloser, error) { return d.openShard(i, dir, name, g) }
	// Each half is a list of readers opened lazily, in order.
	var steps []func() (io.Reader, []io.Closer, error)
	single := func(i int, n int64) func() (io.Reader, []io.Closer, error) {
		return func() (io.Reader, []io.Closer, error) {
			r, err := open(i)
			if err != nil {
				return nil, nil, err
			}
			return io.LimitReader(r, n), []io.Closer{r}, nil
		}
	}
	xor := func(i, j int, n int64) func() (io.Reader, []io.Closer, error) {
		return func() (io.Reader, []io.Closer, error) {
			r1, err := open(i)
			if err != nil {
				return nil, nil, err
			}
			r2, err := open(j)
			if err != nil {
				r1.Close()
				return nil, nil, err
			}
			return &xorReader{a: r1, b: r2, n: n}, []io.Closer{r1, r2}, nil
		}
	}
	switch {
	case g.have[0] && g.have[1]:
		steps = append(steps, single(0, lenA), single(1, lenB))
	case g.have[0]: // A and C
		steps = append(steps, single(0, lenA), xor(0, 2, lenB))
	default: // B and C
		steps = append(steps, xor(2, 1, lenA), single(1, lenB))
	}
	return &stepReader{steps: steps}, nil
}

// xorReader reads n bytes of a XOR b, a shorter b counting as zeros.
type xorReader struct {
	a, b io.Reader
	n    int64
	bEOF bool
	buf  []byte
}

func (x *xorReader) Read(p []byte) (int, error) {
	if x.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > x.n {
		p = p[:x.n]
	}
	n, err := io.ReadFull(x.a, p)
	if err != nil && err != io.ErrUnexpectedEOF {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, err
	}
	p = p[:n]
	if cap(x.buf) < n {
		x.buf = make([]byte, n)
	}
	b := x.buf[:n]
	m := 0
	if !x.bEOF {
		m, err = io.ReadFull(x.b, b)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			x.bEOF = true
		} else if err != nil {
			return 0, err
		}
	}
	for i := range p {
		if i < m {
			p[i] ^= b[i]
		}
	}
	x.n -= int64(n)
	return n, nil
}

// stepReader reads the readers its steps open, one after the other.
type stepReader struct {
	steps   []func() (io.Reader, []io.Closer, error)
	cur     io.Reader
	closers []io.Closer
}

func (s *stepReader) Read(p []byte) (int, error) {
	for {
		if s.cur == nil {
			if len(s.steps) == 0 {
				return 0, io.EOF
			}
			r, cs, err := s.steps[0]()
			if err != nil {
				return 0, err
			}
			s.steps = s.steps[1:]
			s.cur = r
			s.closeAll()
			s.closers = cs
		}
		n, err := s.cur.Read(p)
		if err == io.EOF {
			s.cur = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}

func (s *stepReader) closeAll() {
	for _, c := range s.closers {
		c.Close()
	}
	s.closers = nil
}

func (s *stepReader) Close() error {
	s.closeAll()
	return nil
}

// OpenRandom implements vfs.RandomAccessOpener for reading (writing goes
// through Create: a split file can't change in place).
func (d *DispersedFS) OpenRandom(p string, flag int, perm os.FileMode) (vfs.RandomAccessFile, error) {
	p = clean(p)
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		return nil, pathErr("open", p, syscall.EROFS)
	}
	_, g, isDir, err := d.lookup(p, false)
	if err != nil {
		return nil, err
	}
	if isDir {
		return nil, pathErr("open", p, syscall.EISDIR)
	}
	if g == nil {
		return nil, errNotExist("open", p)
	}
	r := &dispersedRandom{p: p, g: g}
	r.lenA, r.lenB = g.halves()
	for i := range 3 {
		if !g.have[i] {
			continue
		}
		at, c, err := d.shardReaderAt(i, path.Dir(p), path.Base(p), g)
		if err != nil {
			r.Close()
			return nil, err
		}
		r.at[i], r.closers = at, append(r.closers, c)
	}
	return r, nil
}

type dispersedRandom struct {
	p          string
	g          *generation
	lenA, lenB int64
	at         [3]io.ReaderAt
	closers    []io.Closer
}

// readShard reads len(b) bytes of shard i at off, zeros past a shorter B.
func (r *dispersedRandom) readShard(i int, b []byte, off int64) error {
	limit := r.lenA
	if i == 1 {
		limit = r.lenB
	}
	clear(b)
	n := int64(len(b))
	if off >= limit {
		return nil
	}
	if off+n > limit {
		n = limit - off
	}
	_, err := r.at[i].ReadAt(b[:n], off)
	if err == io.EOF {
		err = nil
	}
	return err
}

// half reads from half h (0: A, 1: B) at off, rebuilding it if missing.
func (r *dispersedRandom) half(h int, b []byte, off int64) error {
	if r.at[h] != nil {
		return r.readShard(h, b, off)
	}
	if err := r.readShard(2, b, off); err != nil {
		return err
	}
	other := make([]byte, len(b))
	if err := r.readShard(1-h, other, off); err != nil {
		return err
	}
	for i := range b {
		b[i] ^= other[i]
	}
	return nil
}

func (r *dispersedRandom) ReadAt(b []byte, off int64) (int, error) {
	size := r.lenA + r.lenB
	if off >= size {
		return 0, io.EOF
	}
	want := len(b)
	if off+int64(want) > size {
		b = b[:size-off]
	}
	done := 0
	for done < len(b) {
		pos := off + int64(done)
		chunk := b[done:]
		var err error
		if pos < r.lenA {
			if int64(len(chunk)) > r.lenA-pos {
				chunk = chunk[:r.lenA-pos]
			}
			err = r.half(0, chunk, pos)
		} else {
			err = r.half(1, chunk, pos-r.lenA)
		}
		if err != nil {
			return done, err
		}
		done += len(chunk)
	}
	if done < want {
		return done, io.EOF
	}
	return done, nil
}

func (r *dispersedRandom) WriteAt([]byte, int64) (int, error) {
	return 0, pathErr("write", r.p, syscall.EROFS)
}
func (r *dispersedRandom) Truncate(int64) error { return pathErr("truncate", r.p, syscall.EROFS) }
func (r *dispersedRandom) Close() error {
	for _, c := range r.closers {
		c.Close()
	}
	r.closers = nil
	return nil
}

// ---- writing ----

// Create spools what's written to a temporary file (the vault writes
// encrypted data: nothing in clear) and splits it over the three parts on
// Close, which fails — leaving any previous version intact — unless all
// three take their shard.
func (d *DispersedFS) Create(p string) (io.WriteCloser, error) {
	p = clean(p)
	if err := d.all(); err != nil {
		return nil, err
	}
	if _, _, isDir, err := d.lookup(p, false); err != nil {
		return nil, err
	} else if isDir {
		return nil, pathErr("create", p, syscall.EISDIR)
	}
	spool, err := os.CreateTemp("", "shfm-split-*")
	if err != nil {
		return nil, err
	}
	os.Remove(spool.Name())
	return &dispersedWriter{d: d, p: p, spool: spool}, nil
}

// CreateSized implements vfs.SizedCreator, for the shards' sake on sized
// backends: the size is known once spooled anyway.
func (d *DispersedFS) CreateSized(p string, size int64) (io.WriteCloser, error) {
	return d.Create(p)
}

type dispersedWriter struct {
	d     *DispersedFS
	p     string
	spool *os.File
	n     int64
	done  bool
}

func (w *dispersedWriter) Write(b []byte) (int, error) {
	if w.done {
		return 0, os.ErrClosed
	}
	n, err := w.spool.Write(b)
	w.n += int64(n)
	return n, err
}

func (w *dispersedWriter) Close() error {
	if w.done {
		return os.ErrClosed
	}
	w.done = true
	defer w.spool.Close()
	return w.d.store(w.p, w.spool, w.n)
}

// store splits the n bytes of src into p's shards on the three parts.
func (d *DispersedFS) store(p string, src io.ReaderAt, n int64) error {
	dir, name := path.Dir(p), path.Base(p)
	lenA := (n + 1) / 2
	lenB := n - lenA
	pad := int(lenA - lenB)
	gen := newGen()
	shard := func(i int) (io.Reader, int64) {
		switch i {
		case 0:
			return io.NewSectionReader(src, 0, lenA), lenA
		case 1:
			return io.NewSectionReader(src, lenA, lenB), lenB
		}
		return &xorReader{a: io.NewSectionReader(src, 0, lenA), b: io.NewSectionReader(src, lenA, lenB), n: lenA}, lenA
	}
	var temps, finals [3]string
	var errs [3]error
	var wg sync.WaitGroup
	for i := range d.parts {
		fs := d.parts[i].FS
		pdir := d.partPath(i, dir)
		temps[i] = fs.Join(pdir, shardTemp+gen+fmt.Sprint(i))
		finals[i] = fs.Join(pdir, shardName(name, gen, i, pad))
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, size := shard(i)
			errs[i] = writeStream(fs, temps[i], r, size)
		}()
	}
	wg.Wait()
	cleanup := func() {
		for i := range d.parts {
			removeIfExists(d.parts[i].FS, temps[i])
			removeIfExists(d.parts[i].FS, finals[i])
		}
	}
	for i, err := range errs {
		if err != nil {
			cleanup()
			return fmt.Errorf("part %d (%s): %w", i+1, d.parts[i].FS.Label(), err)
		}
	}
	for i := range d.parts {
		if err := moveFile(d.parts[i].FS, temps[i], finals[i]); err != nil {
			cleanup()
			return fmt.Errorf("part %d (%s): %w", i+1, d.parts[i].FS.Label(), err)
		}
	}
	// The new generation is complete: the older ones can go.
	d.invalidate(dir)
	if l, err := d.list(dir, true); err == nil {
		d.removeShards(l, dir, name, gen)
		d.invalidate(dir)
	}
	return nil
}

// writeStream writes size bytes of r as file p, declaring the size where
// the backend needs it.
func writeStream(fs vfs.FileSystem, p string, r io.Reader, size int64) error {
	var w io.WriteCloser
	var err error
	if sc, ok := fs.(vfs.SizedCreator); ok {
		w, err = sc.CreateSized(p, size)
	} else {
		w, err = fs.Create(p)
	}
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, io.LimitReader(r, size)); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// ---- repair ----

// RepairDispersed makes the three parts of fs whole again — after a part
// was unreachable during changes, or replaced by an empty folder: folders
// missing on a part are created, missing shards rebuilt from the other two,
// leftovers of older writes and of deleted entries removed. All three parts
// must be connected.
func RepairDispersed(fs vfs.FileSystem) (RepairStats, error) {
	var st RepairStats
	d, ok := fs.(*DispersedFS)
	if !ok {
		return st, vfs.ErrNotSupported
	}
	if len(d.Unavailable()) > 0 {
		return st, ErrPartUnavailable
	}
	err := d.repairDir("/", &st)
	return st, err
}

func (d *DispersedFS) repairDir(dir string, st *RepairStats) error {
	l, err := d.list(dir, true)
	if err != nil {
		return err
	}
	if l.ok < 3 {
		return ErrPartUnavailable
	}
	for name, n := range l.dirs {
		p := path.Join(dir, name)
		if n < 2 {
			// A folder on one part only: deleted while that part was away.
			for i := range d.parts {
				if containsName(l.raw[i], name) {
					if err := d.parts[i].FS.Remove(d.partPath(i, p)); err == nil {
						st.Removed++
					}
				}
			}
			continue
		}
		for i := range d.parts {
			if !containsName(l.raw[i], name) {
				if err := d.parts[i].FS.Mkdir(d.partPath(i, p)); err != nil {
					return err
				}
				st.Folders++
			}
		}
		if err := d.repairDir(p, st); err != nil {
			return err
		}
	}
	for name, fe := range l.files {
		if l.dirs[name] >= 2 {
			continue
		}
		g := fe.best
		if g == nil {
			st.Lost++
			continue
		}
		if g.count() < 3 {
			if err := d.rebuild(dir, name, g); err != nil {
				return err
			}
			st.Shards += 3 - g.count()
		}
		for gen, other := range fe.gens {
			if gen != g.gen {
				st.Removed += other.count()
			}
		}
		d.removeShards(l, dir, name, g.gen)
	}
	d.invalidate(dir)
	return nil
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// rebuild writes the shards g lacks, from the file read back whole.
func (d *DispersedFS) rebuild(dir, name string, g *generation) error {
	p := path.Join(dir, name)
	r, err := d.Open(p)
	if err != nil {
		return err
	}
	spool, err := os.CreateTemp("", "shfm-split-*")
	if err != nil {
		r.Close()
		return err
	}
	os.Remove(spool.Name())
	defer spool.Close()
	n, err := io.Copy(spool, r)
	r.Close()
	if err != nil {
		return err
	}
	lenA := (n + 1) / 2
	lenB := n - lenA
	pad := int(lenA - lenB)
	for i := range d.parts {
		if g.have[i] {
			continue
		}
		var src io.Reader
		var size int64
		switch i {
		case 0:
			src, size = io.NewSectionReader(spool, 0, lenA), lenA
		case 1:
			src, size = io.NewSectionReader(spool, lenA, lenB), lenB
		default:
			src = &xorReader{a: io.NewSectionReader(spool, 0, lenA), b: io.NewSectionReader(spool, lenA, lenB), n: lenA}
			size = lenA
		}
		fs := d.parts[i].FS
		tmp := fs.Join(d.partPath(i, dir), shardTemp+g.gen+fmt.Sprint(i))
		if err := writeStream(fs, tmp, src, size); err != nil {
			removeIfExists(fs, tmp)
			return err
		}
		if err := moveFile(fs, tmp, fs.Join(d.partPath(i, dir), shardName(name, g.gen, i, pad))); err != nil {
			removeIfExists(fs, tmp)
			return err
		}
	}
	return nil
}
