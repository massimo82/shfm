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

package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// EntryType is what an archive entry is.
type EntryType int

const (
	TypeFile EntryType = iota
	TypeDir
	TypeSymlink
	// TypeHardlink is a second name for an earlier entry of the same
	// archive (tar only): Linkname is that entry's Name.
	TypeHardlink
)

// Entry is one member of an archive, as Walk reports it.
type Entry struct {
	// Name is the entry's path inside the archive: relative, '/'-separated
	// and cleaned — never absolute and never climbing above the archive's
	// root with "..", which Walk reports as Skip instead.
	Name    string
	Type    EntryType
	Mode    fs.FileMode // permission bits only; 0 when the format has none
	ModTime time.Time   // zero when unknown
	Size    int64       // a file's uncompressed size, -1 when unknown
	// Linkname is a symlink's target exactly as stored (see SafeSymlink),
	// or a hardlink's target entry, cleaned like Name.
	Linkname string
	// Skip, when non-nil, says why this entry can't be extracted (an
	// unsafe name, an encrypted or special file...): it comes with no
	// content, and Name may then be the raw, uncleaned name.
	Skip error
}

// Source is an archive to read: from a local path when there is one,
// otherwise through Open (any VFS backend). Formats that need random
// access (ZIP, 7z, RAR) are first copied into TempDir in that case.
type Source struct {
	Name      string // file name (or path): its suffix picks the format
	LocalPath string
	Open      func() (io.ReadCloser, error)
	// TempDir is where temporary copies go ("" = the system default).
	TempDir string
}

// ErrStop, returned by Walk's callback, stops the walk early without
// making Walk fail.
var ErrStop = errors.New("archive: stop walking")

// Walk calls fn for every entry of src, in archive order. For a file, r
// streams its content and is only valid until fn returns; it is nil for
// any other entry, and for one with Skip set. A single compressed file
// (".gz", ".xz"...) is reported as one file, named after the archive
// minus its suffix (for gzip, the name in its header when there is one).
//
// An error returned by fn stops the walk and is returned as is, except
// ErrStop, which makes Walk return nil. Cancelling ctx stops the walk too,
// killing any external tool it started.
func Walk(ctx context.Context, src Source, fn func(e Entry, r io.Reader) error) error {
	kind, base := Detect(src.Name)
	if kind == "" {
		return fmt.Errorf("%s: not a recognised archive", src.Name)
	}
	if err := missingTool(kind); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var err error
	switch {
	case kind == KindZip:
		err = walkZip(ctx, src, fn)
	case kind == KindSevenZip || kind == KindRar:
		err = walkExternal(ctx, src, kind, fn)
	case kind.Single():
		err = walkSingle(ctx, src, kind, base, fn)
	default:
		err = walkTarKind(ctx, src, kind, fn)
	}
	if errors.Is(err, ErrStop) {
		return nil
	}
	if err == nil {
		err = ctx.Err()
	}
	return err
}

// ErrUnsafePath is (wrapped in) the Skip of an entry whose name climbs
// above the archive's root.
var ErrUnsafePath = errors.New("path outside the archive's folder")

// cleanName turns an entry's stored name into Entry.Name: leading slashes
// dropped (as GNU tar does) and cleaned, refusing one that climbs out
// with "..". The archive's root itself comes back as ".".
func cleanName(raw string) (string, error) {
	if strings.IndexByte(raw, 0) >= 0 {
		return raw, fmt.Errorf("%q: invalid name", raw)
	}
	n := path.Clean(strings.TrimLeft(raw, "/"))
	if n == ".." || strings.HasPrefix(n, "../") {
		return raw, fmt.Errorf("%q: %w", raw, ErrUnsafePath)
	}
	return n, nil
}

// SafeSymlink reports whether a symlink entry named name pointing at
// target stays inside the archive's folder once extracted, whatever other
// symlinks it goes through: target is relative, climbs with ".." only at
// its start (a ".." after a component that may itself be a symlink
// wouldn't go where it reads) and not above the archive's root. This
// holds as long as the folders above name are real folders, not links.
func SafeSymlink(name, target string) bool {
	if target == "" || path.IsAbs(target) || strings.IndexByte(target, 0) >= 0 {
		return false
	}
	depth := strings.Count(name, "/") // folders above name, inside the root
	climbing := true
	for _, c := range strings.Split(target, "/") {
		switch {
		case c == "..":
			if !climbing || depth == 0 {
				return false
			}
			depth--
		case c != "" && c != ".":
			climbing = false
		}
	}
	return true
}

func (s Source) open() (io.ReadCloser, error) {
	if s.LocalPath != "" {
		return os.Open(s.LocalPath)
	}
	if s.Open == nil {
		return nil, fmt.Errorf("%s: no way to open it", s.Name)
	}
	return s.Open()
}

// localFile returns a local path holding src — its own, or a temporary
// copy (removed by cleanup) when it has none.
func (s Source) localFile(ctx context.Context) (p string, cleanup func(), err error) {
	if s.LocalPath != "" {
		return s.LocalPath, func() {}, nil
	}
	rc, err := s.open()
	if err != nil {
		return "", nil, err
	}
	defer rc.Close()
	ext := path.Ext(s.Name)
	tmp, err := os.CreateTemp(s.TempDir, "shfm-archive-*"+ext)
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.Remove(tmp.Name()) }
	_, err = io.Copy(tmp, ctxReader{ctx, rc})
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp.Name(), cleanup, nil
}

// ctxReader fails reads once ctx is cancelled, so copying a long stream
// stops promptly.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// --- ZIP ------------------------------------------------------------------

func walkZip(ctx context.Context, src Source, fn func(Entry, io.Reader) error) error {
	p, cleanup, err := src.localFile(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	zr, err := zip.OpenReader(p)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The ZIP spec mandates '/', so a backslash is always a
		// separator from an archiver on Windows, never part of a name.
		raw := strings.ReplaceAll(f.Name, `\`, "/")
		mode := f.Mode()
		e := Entry{Mode: mode.Perm(), ModTime: f.Modified, Size: -1}
		name, err := cleanName(raw)
		e.Name, e.Skip = name, err
		switch {
		case mode.IsDir() || strings.HasSuffix(raw, "/"):
			e.Type = TypeDir
		case mode&fs.ModeSymlink != 0:
			e.Type = TypeSymlink
			if e.Skip == nil {
				e.Linkname, e.Skip = zipSymlinkTarget(f)
			}
		case !mode.IsRegular():
			e.Skip = fmt.Errorf("%s: special file not extracted", name)
		default:
			e.Size = int64(f.UncompressedSize64)
			if f.Flags&0x1 != 0 && e.Skip == nil {
				e.Skip = fmt.Errorf("%s: encrypted, not supported", name)
			}
		}
		if e.Name == "." && e.Type == TypeDir {
			continue
		}
		if e.Type != TypeFile || e.Skip != nil {
			if err := fn(e, nil); err != nil {
				return err
			}
			continue
		}
		rc, err := f.Open()
		if err != nil {
			e.Skip = fmt.Errorf("%s: %w", name, err)
			if err := fn(e, nil); err != nil {
				return err
			}
			continue
		}
		err = fn(e, ctxReader{ctx, rc})
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// zipSymlinkTarget reads a ZIP symlink entry's target, stored as its
// content.
func zipSymlinkTarget(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, 4096))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// --- TAR and single compressed files -------------------------------------

// walkTar reports every entry of the tar stream r.
func walkTar(r io.Reader, fn func(Entry, io.Reader) error) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		e := Entry{Mode: fs.FileMode(hdr.Mode).Perm(), ModTime: hdr.ModTime, Size: -1}
		name, err := cleanName(hdr.Name)
		e.Name, e.Skip = name, err
		switch hdr.Typeflag {
		case tar.TypeReg, 0: // 0 is the pre-POSIX spelling of a regular file
			e.Type, e.Size = TypeFile, hdr.Size
		case tar.TypeDir:
			e.Type = TypeDir
		case tar.TypeSymlink:
			e.Type, e.Linkname = TypeSymlink, hdr.Linkname
		case tar.TypeLink:
			e.Type = TypeHardlink
			target, err := cleanName(hdr.Linkname)
			e.Linkname = target
			if e.Skip == nil {
				e.Skip = err
			}
		case tar.TypeXGlobalHeader:
			continue
		default:
			if e.Skip == nil {
				e.Skip = fmt.Errorf("%s: special file not extracted", name)
			}
		}
		if e.Name == "." && e.Type == TypeDir {
			continue
		}
		var content io.Reader
		if e.Type == TypeFile && e.Skip == nil {
			content = tr
		}
		if err := fn(e, content); err != nil {
			return err
		}
	}
}

func walkTarKind(ctx context.Context, src Source, kind Kind, fn func(Entry, io.Reader) error) error {
	filter := kind.tarFilter()
	rc, err := src.open()
	if err != nil {
		return err
	}
	defer rc.Close()
	var r io.Reader = ctxReader{ctx, rc}

	if filter != "" && filter != KindGzip && filter != KindBzip2 && toolPath(filterTool(filter)) == "" {
		// No dedicated decompressor: bsdtar (which missingTool made
		// sure exists) rewrites the stream as a plain tar.
		p := startTool(ctx, toolPath("bsdtar"), r, bsdtarArgs("-")...)
		return p.finish(walkTar(p.out, fn))
	}
	return decompressed(ctx, r, filter, func(r io.Reader) error { return walkTar(r, fn) })
}

func walkSingle(ctx context.Context, src Source, kind Kind, base string, fn func(Entry, io.Reader) error) error {
	rc, err := src.open()
	if err != nil {
		return err
	}
	defer rc.Close()
	var r io.Reader = ctxReader{ctx, rc}

	e := Entry{Name: base, Type: TypeFile, Size: -1}
	if kind == KindGzip {
		// Read the header here, for the original name and time.
		gz, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer gz.Close()
		if n := path.Base(strings.ReplaceAll(gz.Name, `\`, "/")); gz.Name != "" && n != "." && n != ".." && n != "/" {
			e.Name = n
		}
		e.ModTime = gz.ModTime
		r, kind = gz, ""
	}
	if name, err := cleanName(e.Name); err != nil || name == "." {
		return fmt.Errorf("%s: can't name the file inside it", src.Name)
	}
	return decompressed(ctx, r, kind, func(r io.Reader) error { return fn(e, r) })
}

// decompressed runs use on r decompressed as kind, a single-file kind (""
// for none): gzip and bzip2 in process, anything else through its
// external tool.
func decompressed(ctx context.Context, r io.Reader, kind Kind, use func(io.Reader) error) error {
	switch kind {
	case "":
		return use(r)
	case KindGzip:
		gz, err := gzip.NewReader(r)
		if err != nil {
			return err
		}
		defer gz.Close()
		return use(gz)
	case KindBzip2:
		return use(bzip2.NewReader(r))
	}
	p := startTool(ctx, toolPath(filterTool(kind)), r, "-dc")
	return p.finish(use(p.out))
}

// --- external tools -------------------------------------------------------

// bsdtarArgs makes bsdtar re-emit the archive at p ("-" for stdin), of any
// format it reads, as a plain tar stream on stdout.
func bsdtarArgs(p string) []string {
	return []string{"-c", "-f", "-", "--format", "pax", "@" + p}
}

// toolProc is an external tool whose stdout is being read.
type toolProc struct {
	cmd    *exec.Cmd
	out    io.Reader
	stderr bytes.Buffer
	cancel context.CancelFunc
	err    error // from starting it
}

// startTool starts tool with args, stdin (nil for none) as its input. It
// runs in a session of its own, without a controlling terminal, so a tool
// asking for a password (an encrypted archive) fails instead of reading
// from shfm's own terminal.
func startTool(ctx context.Context, tool string, stdin io.Reader, args ...string) *toolProc {
	ctx, cancel := context.WithCancel(ctx)
	p := &toolProc{cancel: cancel}
	p.cmd = exec.CommandContext(ctx, tool, args...)
	p.cmd.Stdin = stdin
	p.cmd.Stderr = &p.stderr
	detach(p.cmd)
	out, err := p.cmd.StdoutPipe()
	if err == nil {
		err = p.cmd.Start()
	}
	if err != nil {
		p.err = err
		p.out = bytes.NewReader(nil)
		return p
	}
	p.out = out
	return p
}

// finish waits for the tool once its output was used, readErr being how
// that went: on success the rest of the output is drained first (the tool
// can't exit while blocked writing it), otherwise the tool is killed and
// whatever it printed is added to readErr, since a failing tool (a
// corrupt or encrypted archive) is what usually truncated the stream.
func (p *toolProc) finish(readErr error) error {
	defer p.cancel()
	if p.err != nil {
		return p.err
	}
	tool := filepath.Base(p.cmd.Path)
	if readErr != nil {
		p.cancel()
		_ = p.cmd.Wait()
		if msg := strings.TrimSpace(p.stderr.String()); msg != "" && !errors.Is(readErr, ErrStop) {
			return fmt.Errorf("%w (%s: %s)", readErr, tool, msg)
		}
		return readErr
	}
	_, _ = io.Copy(io.Discard, p.out)
	if err := p.cmd.Wait(); err != nil {
		if msg := strings.TrimSpace(p.stderr.String()); msg != "" {
			return fmt.Errorf("%s: %s", tool, msg)
		}
		return fmt.Errorf("%s: %w", tool, err)
	}
	return nil
}

// walkExternal reads a 7z or RAR archive: through bsdtar as a tar stream
// when installed, otherwise by extracting it whole with 7-Zip or unrar
// into a temporary folder that is then walked.
func walkExternal(ctx context.Context, src Source, kind Kind, fn func(Entry, io.Reader) error) error {
	p, cleanup, err := src.localFile(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	if bsdtar := toolPath("bsdtar"); bsdtar != "" {
		proc := startTool(ctx, bsdtar, nil, bsdtarArgs(p)...)
		return proc.finish(walkTar(proc.out, fn))
	}

	stage, err := os.MkdirTemp(src.TempDir, "shfm-archive-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	var proc *toolProc
	if unrar := toolPath("unrar"); kind == KindRar && unrar != "" {
		// -p-: never ask for a password.
		proc = startTool(ctx, unrar, nil, "x", "-o+", "-p-", "-idq", "--", p, stage+"/")
	} else {
		proc = startTool(ctx, toolPath("7z"), nil, "x", "-y", "-bd", "-o"+stage, "--", p)
	}
	if err := proc.finish(nil); err != nil {
		return err
	}
	return walkDir(ctx, stage, fn)
}

// walkDir reports the content of a local folder as archive entries, never
// following symlinks (they are reported as such).
func walkDir(ctx context.Context, root string, fn func(Entry, io.Reader) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := Entry{Name: filepath.ToSlash(rel), Mode: info.Mode().Perm(), ModTime: info.ModTime(), Size: -1}
		switch {
		case d.IsDir():
			e.Type = TypeDir
			return fn(e, nil)
		case d.Type()&fs.ModeSymlink != 0:
			e.Type = TypeSymlink
			e.Linkname, e.Skip = os.Readlink(p)
			return fn(e, nil)
		case !d.Type().IsRegular():
			e.Skip = fmt.Errorf("%s: special file not extracted", e.Name)
			return fn(e, nil)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		e.Size = info.Size()
		return fn(e, ctxReader{ctx, f})
	})
}
