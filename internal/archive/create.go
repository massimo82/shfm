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
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// creatable lists the formats Create writes, in the order a menu offers
// them. RAR is missing because its compressor is proprietary, .tar.lzma
// because xz superseded it, and single compressed files (".gz") because an
// archive of one file does the same job.
var creatable = []Kind{
	KindZip, KindTarGz, KindTarXz, KindTarZstd, KindTarBz2, KindSevenZip,
	KindTar, KindTarLzip, KindTarLz4,
}

// CreateKinds returns every format Create writes, in menu order, whether
// or not this machine can: see CreateNeeds.
func CreateKinds() []Kind {
	return append([]Kind(nil), creatable...)
}

// Creatable returns the formats Create can write on this machine, in menu
// order: ZIP, TAR and TAR.GZ always; the others when their compressor (or
// bsdtar) is installed.
func Creatable() []Kind {
	var out []Kind
	for _, k := range creatable {
		if missingCreateTool(k) == nil {
			out = append(out, k)
		}
	}
	return out
}

// CreateNeeds says what to install to write kind ("lzip or bsdtar"), ""
// when this machine already can.
func CreateNeeds(kind Kind) string {
	return orList(createNeeds(kind))
}

// Ext returns the filename suffix an archive of kind is given
// (".tar.gz", ".zip"...).
func (k Kind) Ext() string {
	for _, s := range suffixes {
		if s.kind == k {
			return s.suffix
		}
	}
	return ""
}

// Label describes kind for a menu.
func (k Kind) Label() string {
	switch k {
	case KindZip:
		return ".zip — ZIP, opens everywhere"
	case KindTar:
		return ".tar — no compression"
	case KindTarGz:
		return ".tar.gz — gzip, fast"
	case KindTarBz2:
		return ".tar.bz2 — bzip2"
	case KindTarXz:
		return ".tar.xz — xz, smallest, slow"
	case KindTarZstd:
		return ".tar.zst — zstd, small and fast"
	case KindTarLzip:
		return ".tar.lz — lzip"
	case KindTarLz4:
		return ".tar.lz4 — lz4, fastest"
	case KindSevenZip:
		return ".7z — 7-Zip"
	}
	return k.Ext()
}

// compressTools maps a compression layer to its compressor tool and
// arguments (writing stdin, compressed, to stdout), and to bsdtar's flag
// for the same compression.
var compressTools = map[Kind]struct {
	tool      string
	args      []string
	bsdtarArg string
}{
	KindBzip2: {"bzip2", []string{"-c"}, "--bzip2"},
	KindXz:    {"xz", []string{"-c", "-T0"}, "--xz"},
	KindZstd:  {"zstd", []string{"-c", "-q", "-T0"}, "--zstd"},
	KindLzip:  {"lzip", []string{"-c"}, "--lzip"},
	KindLz4:   {"lz4", []string{"-c", "-q"}, "--lz4"},
}

// createNeeds returns the tools any one of which writing kind needs, nil
// when this machine already can.
func createNeeds(kind Kind) []string {
	switch kind {
	case KindZip, KindTar, KindTarGz:
		return nil
	case KindSevenZip:
		if toolPath("bsdtar") != "" || toolPath("7z") != "" {
			return nil
		}
		return []string{"bsdtar", "7-Zip"}
	}
	if c, ok := compressTools[kind.tarFilter()]; ok && toolPath(c.tool) == "" && toolPath("bsdtar") == "" {
		return []string{c.tool, "bsdtar"}
	}
	return nil
}

// missingCreateTool explains what needs installing to write kind, nil
// when nothing does.
func missingCreateTool(kind Kind) error {
	if !slices.Contains(creatable, kind) {
		return fmt.Errorf("can't create %s archives", kind)
	}
	return missingError("creating", kind, createNeeds(kind))
}

// PutFunc adds one entry to the archive being created: a file's content
// is r, exactly e.Size bytes; r is ignored for a folder or a symlink
// (whose target is e.Linkname). e.Name is the entry's '/'-separated path
// inside the archive.
type PutFunc func(e Entry, r io.Reader) error

// Create writes an archive of kind to out, with the entries add puts in
// it. tempDir ("" = the system default) is where bsdtar writes its output,
// or a 7z is staged when only the 7-Zip command is there to write it. Cancelling ctx
// stops it; out then holds a truncated archive.
func Create(ctx context.Context, kind Kind, out io.Writer, tempDir string, add func(put PutFunc) error) error {
	if err := missingCreateTool(kind); err != nil {
		return err
	}
	switch {
	case kind == KindZip:
		return createZip(out, add)
	case kind == KindSevenZip:
		if toolPath("bsdtar") != "" {
			return throughBsdtar(ctx, []string{"--format", "7zip"}, out, tempDir, add)
		}
		return createSevenZip(ctx, out, tempDir, add)
	case kind == KindTar:
		return createTar(out, add)
	case kind == KindTarGz:
		gz := gzip.NewWriter(out)
		if err := createTar(gz, add); err != nil {
			return err
		}
		return gz.Close()
	}
	c := compressTools[kind.tarFilter()]
	if tool := toolPath(c.tool); tool != "" {
		return throughTool(ctx, tool, c.args, out, add)
	}
	return throughBsdtar(ctx, []string{"--format", "pax", c.bsdtarArg}, out, tempDir, add)
}

// throughBsdtar has bsdtar, given formatArgs, convert a tar stream of
// add's entries, copying the result to out. bsdtar writes into a
// temporary file: on stdout it pads the output to a whole 10KiB block,
// which zstd, for one, rejects as trailing garbage.
func throughBsdtar(ctx context.Context, formatArgs []string, out io.Writer, tempDir string, add func(PutFunc) error) error {
	dir, err := os.MkdirTemp(tempDir, "shfm-archive-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	result := filepath.Join(dir, "out")
	args := append(append([]string{"-c", "-f", result}, formatArgs...), "@-")
	if err := throughTool(ctx, toolPath("bsdtar"), args, io.Discard, add); err != nil {
		return err
	}
	f, err := os.Open(result)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(out, ctxReader{ctx, f})
	return err
}

// createTar writes a tar stream of add's entries to w.
func createTar(w io.Writer, add func(PutFunc) error) error {
	tw := tar.NewWriter(w)
	err := add(func(e Entry, r io.Reader) error {
		hdr := &tar.Header{Name: e.Name, Mode: int64(e.Mode.Perm()), ModTime: e.ModTime}
		switch e.Type {
		case TypeDir:
			hdr.Typeflag, hdr.Name = tar.TypeDir, e.Name+"/"
			if hdr.Mode == 0 {
				hdr.Mode = 0o755
			}
		case TypeSymlink:
			hdr.Typeflag, hdr.Linkname, hdr.Mode = tar.TypeSymlink, e.Linkname, 0o777
		default:
			hdr.Typeflag, hdr.Size = tar.TypeReg, e.Size
			if hdr.Mode == 0 {
				hdr.Mode = 0o644
			}
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if e.Type != TypeFile {
			return nil
		}
		n, err := io.Copy(tw, r)
		if err == nil && n != e.Size {
			err = fmt.Errorf("%s changed size while being archived", e.Name)
		}
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

func createZip(w io.Writer, add func(PutFunc) error) error {
	zw := zip.NewWriter(w)
	err := add(func(e Entry, r io.Reader) error {
		hdr := &zip.FileHeader{Name: e.Name, Modified: e.ModTime, Method: zip.Deflate}
		mode := e.Mode.Perm()
		switch e.Type {
		case TypeDir:
			hdr.Name, hdr.Method = e.Name+"/", zip.Store
			if mode == 0 {
				mode = 0o755
			}
			hdr.SetMode(fs.ModeDir | mode)
		case TypeSymlink:
			hdr.Method = zip.Store
			hdr.SetMode(fs.ModeSymlink | 0o777)
			r = strings.NewReader(e.Linkname)
		default:
			if mode == 0 {
				mode = 0o644
			}
			hdr.SetMode(mode)
		}
		fw, err := zw.CreateHeader(hdr)
		if err != nil || e.Type == TypeDir {
			return err
		}
		_, err = io.Copy(fw, r)
		return err
	})
	if err != nil {
		return err
	}
	return zw.Close()
}

// throughTool pipes a tar stream of add's entries through tool (a
// compressor, or bsdtar converting it), whose output goes to out.
func throughTool(ctx context.Context, tool string, args []string, out io.Writer, add func(PutFunc) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, args...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = out, &stderr
	detach(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	err = createTar(stdin, add)
	if cerr := stdin.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	msg := strings.TrimSpace(stderr.String())
	switch {
	case err != nil && msg != "":
		return fmt.Errorf("%w (%s: %s)", err, filepath.Base(tool), msg)
	case err != nil:
		return err
	case waitErr != nil && msg != "":
		return fmt.Errorf("%s: %s", filepath.Base(tool), msg)
	case waitErr != nil:
		return fmt.Errorf("%s: %w", filepath.Base(tool), waitErr)
	}
	return ctx.Err()
}

// stageTimes gives a staged file or folder its entry's modification time,
// best-effort: the content matters more.
func stageTimes(p string, modTime time.Time) {
	if !modTime.IsZero() {
		_ = os.Chtimes(p, modTime, modTime)
	}
}

// createSevenZip writes a 7z with the 7-Zip command, which only archives
// real files: add's entries are first written into a staging folder.
func createSevenZip(ctx context.Context, out io.Writer, tempDir string, add func(PutFunc) error) error {
	stage, err := os.MkdirTemp(tempDir, "shfm-archive-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	content := filepath.Join(stage, "content")
	if err := os.Mkdir(content, 0o700); err != nil {
		return err
	}
	var names []string
	var dirs []Entry // their attributes are set once their content is in
	err = add(func(e Entry, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := filepath.Join(content, filepath.FromSlash(e.Name))
		if !strings.Contains(e.Name, "/") {
			names = append(names, e.Name)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		switch e.Type {
		case TypeDir:
			dirs = append(dirs, e)
			return os.MkdirAll(p, 0o755)
		case TypeSymlink:
			return os.Symlink(e.Linkname, p)
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, ctxReader{ctx, r})
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		// 7-Zip stores what it finds on disk. Readable by its owner, or
		// 7-Zip couldn't archive it.
		if mode := e.Mode.Perm(); mode != 0 {
			if err := os.Chmod(p, mode|0o400); err != nil {
				return err
			}
		}
		stageTimes(p, e.ModTime)
		return nil
	})
	if err != nil {
		return err
	}
	for _, d := range dirs {
		p := filepath.Join(content, filepath.FromSlash(d.Name))
		// Kept usable by its owner, as extracting makes it anyway: 7-Zip
		// must list it, and the staging folder be removed.
		if mode := d.Mode.Perm(); mode != 0 {
			if err := os.Chmod(p, mode|0o700); err != nil {
				return err
			}
		}
		stageTimes(p, d.ModTime)
	}
	if len(names) == 0 {
		return errors.New("nothing to archive")
	}

	result := filepath.Join(stage, "out.7z")
	// -snl stores symlinks as links; "--" ends the switches, so a name
	// starting with "-" is still a name.
	args := append([]string{"a", "-t7z", "-bd", "-y", "-snl", result, "--"}, names...)
	cmd := exec.CommandContext(ctx, toolPath("7z"), args...)
	cmd.Dir = content
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	detach(cmd)
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("7z: %s", msg)
		}
		return fmt.Errorf("7z: %w", err)
	}
	f, err := os.Open(result)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(out, ctxReader{ctx, f})
	return err
}
