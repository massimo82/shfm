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

package fileops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"shfm/internal/archive"
	"shfm/internal/vfs"
)

// ArchiveRoot returns the folder every entry of an archive named name is
// put under: the name minus its extension ("photos.tar.gz" → "photos"),
// so extracting it anywhere never scatters its content.
func ArchiveRoot(name string) string {
	_, base := archive.Detect(name)
	return base
}

// CreateArchive writes an archive of kind at destPath on destFS holding
// items (files and folders, recursively), all under one root folder named
// after the archive (see ArchiveRoot). Progress is reported per item. An
// entry that can't be read before it's added is skipped and reported as
// the result's error; any other failure, or a cancellation, removes the
// incomplete archive.
func CreateArchive(items []Item, destFS vfs.FileSystem, destPath string, kind archive.Kind, prog *Progress) *Result {
	res := &Result{}
	total := len(items)
	root := ArchiveRoot(destFS.Base(destPath))
	c := &compressor{prog: prog, total: total}

	err := c.write(destFS, destPath, kind, func(put archive.PutFunc) error {
		if err := put(archive.Entry{Name: root, Type: archive.TypeDir, Mode: 0o755, ModTime: time.Now()}, nil); err != nil {
			return err
		}
		for i, it := range items {
			if prog.cancelled() {
				return errCancelled
			}
			c.done = i
			name := it.FS.Base(it.Path)
			if err := c.add(put, it.FS, it.Path, root+"/"+name); err != nil {
				return err
			}
			prog.report(i+1, total, name, nil)
		}
		return nil
	})
	switch {
	case errors.Is(err, errCancelled) || errors.Is(err, context.Canceled):
		res.Cancelled = true
	case err != nil:
		res.Errors = append(res.Errors, fmt.Errorf("creating %q: %w", destPath, err))
	case len(c.skipped) > 0:
		res.Done = total
		res.Errors = append(res.Errors, fmt.Errorf("creating %q: %w", destPath, skippedError(c.skipped)))
	default:
		res.Done = total
	}
	return res
}

type compressor struct {
	prog       *Progress
	done       int
	total      int
	skipped    []string
	lastReport time.Time
}

// write runs archive.Create into destPath, removing it on failure. A
// backend that must know a file's size before writing it (MTP) gets the
// archive built in a temporary file first.
func (c *compressor) write(destFS vfs.FileSystem, destPath string, kind archive.Kind, add func(archive.PutFunc) error) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := watchCancel(c.prog.cancelled, cancel)
	defer stop()

	tempDir := ""
	if lp, ok := destFS.(vfs.LocalPath); ok {
		if p, ok := lp.LocalPath(destFS.Dir(destPath)); ok {
			tempDir = p // see extractOne: the system temp may be a tmpfs
		}
	}

	if _, sized := destFS.(vfs.SizedCreator); sized {
		tmp, err := os.CreateTemp("", "shfm-archive-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		if err := archive.Create(ctx, kind, tmp, tempDir, add); err != nil {
			return err
		}
		size, err := tmp.Seek(0, io.SeekEnd)
		if err == nil {
			_, err = tmp.Seek(0, io.SeekStart)
		}
		if err != nil {
			return err
		}
		return c.commit(destFS, destPath, func(w io.Writer) error {
			_, err := io.Copy(w, tmp)
			return err
		}, size)
	}
	return c.commit(destFS, destPath, func(w io.Writer) error {
		return archive.Create(ctx, kind, w, tempDir, add)
	}, -1)
}

// commit creates destPath and fills it with fill, removing it if that
// fails.
func (c *compressor) commit(destFS vfs.FileSystem, destPath string, fill func(io.Writer) error, size int64) error {
	var w io.WriteCloser
	var err error
	if size >= 0 {
		w, err = openDest(destFS, destPath, size)
	} else {
		w, err = destFS.Create(destPath)
	}
	if err != nil {
		return err
	}
	err = fill(w)
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = destFS.Remove(destPath)
	}
	return err
}

// add puts srcPath (a file, symlink or folder, recursively) into the
// archive as name.
func (c *compressor) add(put archive.PutFunc, srcFS vfs.FileSystem, srcPath, name string) error {
	if c.prog.cancelled() {
		return errCancelled
	}
	if now := time.Now(); now.Sub(c.lastReport) > 150*time.Millisecond {
		c.lastReport = now
		c.prog.report(c.done, c.total, name, nil)
	}
	st, err := srcFS.Stat(srcPath)
	if err != nil {
		c.skipped = append(c.skipped, fmt.Sprintf("%s: %v", name, err))
		return nil
	}
	e := archive.Entry{Name: name, Mode: st.Mode.Perm(), ModTime: st.ModTime, Size: st.Size}

	if st.IsSymlink {
		if lp, ok := srcFS.(vfs.LocalPath); ok {
			if p, ok := lp.LocalPath(srcPath); ok {
				target, err := os.Readlink(p)
				if err != nil {
					c.skipped = append(c.skipped, fmt.Sprintf("%s: %v", name, err))
					return nil
				}
				e.Type, e.Linkname = archive.TypeSymlink, target
				return put(e, nil)
			}
		}
		if st.IsDir {
			// A backend without links to store: following one to a
			// folder could loop forever.
			c.skipped = append(c.skipped, fmt.Sprintf("%s: link to a folder, not followed", name))
			return nil
		}
		// A link to a file: its content is archived.
	}

	if st.IsDir {
		children, err := srcFS.List(srcPath)
		if err != nil {
			c.skipped = append(c.skipped, fmt.Sprintf("%s: %v", name, err))
			return nil
		}
		e.Type = archive.TypeDir
		if err := put(e, nil); err != nil {
			return err
		}
		for _, ch := range children {
			if err := c.add(put, srcFS, srcFS.Join(srcPath, ch.Name), name+"/"+ch.Name); err != nil {
				return err
			}
		}
		return nil
	}

	if !st.Mode.IsRegular() && st.Mode&os.ModeSymlink == 0 {
		c.skipped = append(c.skipped, fmt.Sprintf("%s: special file not archived", name))
		return nil
	}
	r, err := srcFS.Open(srcPath)
	if err != nil {
		c.skipped = append(c.skipped, fmt.Sprintf("%s: %v", name, err))
		return nil
	}
	defer r.Close()
	e.Type = archive.TypeFile
	var content io.Reader = r
	if st.SizeUnknown {
		// The archive's header declares the size before the content
		// (a Google Docs document, exported on the fly): spool it first.
		tmp, err := os.CreateTemp("", "shfm-archive-entry-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		n, err := io.Copy(tmp, cancelReader{c.prog.cancelled, r})
		if err != nil {
			if errors.Is(err, errCancelled) {
				return err
			}
			c.skipped = append(c.skipped, fmt.Sprintf("%s: %v", name, err))
			return nil
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		e.Size, content = n, tmp
	}
	return put(e, cancelReader{c.prog.cancelled, content})
}

// cancelReader fails reads once the user cancelled, so archiving a huge
// file stops promptly.
type cancelReader struct {
	cancelled func() bool
	r         io.Reader
}

func (c cancelReader) Read(p []byte) (int, error) {
	if c.cancelled() {
		return 0, errCancelled
	}
	return c.r.Read(p)
}

// watchCancel calls cancel as soon as cancelled reports true, until the
// returned stop is called.
func watchCancel(cancelled func() bool, cancel context.CancelFunc) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if cancelled() {
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}
