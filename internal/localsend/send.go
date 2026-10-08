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

//go:build localsend

package localsend

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"shfm/internal/fileops"
	"shfm/internal/localsend/protocol"
	"shfm/internal/localsend/protocol/lsv2"
	"shfm/internal/vfs"
)

// maxMessage is the longest text sent as a message: longer, it's sent
// as a .txt file (as the reference implementation does).
const maxMessage = 64000

// maxWalkDepth bounds the folders walked into when sending one.
const maxWalkDepth = 64

var (
	errPINCancelled = errors.New("no PIN given")
	errNoFiles      = errors.New("nothing to send: only empty folders")
)

// sending is a transfer being sent, which its receiver may cancel.
type sending struct {
	ip     string
	cancel context.CancelFunc

	mu              sync.Mutex
	sessionID       string
	cancelledByPeer bool
}

// outFile is a file to send: on a source, or spooled to a temporary file
// (a file whose size can't be known before reading it all).
type outFile struct {
	file protocol.File
	fs   vfs.FileSystem
	path string
	temp string
	data []byte // a text sent as a file
}

func (o *outFile) open() (io.ReadCloser, error) {
	switch {
	case o.data != nil:
		return io.NopCloser(bytes.NewReader(o.data)), nil
	case o.temp != "":
		return os.Open(o.temp)
	}
	return o.fs.Open(o.path)
}

// clientFor returns the protocol client speaking dev's version.
func (s *Service) clientFor(dev Device) (protocol.Client, error) {
	if lsv2.Speaks(dev.Version) {
		return s.v2cli, nil
	}
	return nil, fmt.Errorf("protocol %s not supported", dev.Version)
}

// Send sends items (files, and folders with what's inside them) to dev,
// asking ask for a PIN if dev wants one. It returns once every file was
// sent or failed, dev declined, or prog.Cancelled.
func (s *Service) Send(dev Device, items []fileops.Item, ask PINAsker, prog *fileops.Progress) *fileops.Result {
	return s.send(dev, ask, prog, func(ctx context.Context) ([]*outFile, []error) {
		return collect(ctx, items)
	})
}

// SendText sends a text message to dev: in the request itself, or as a
// .txt file if it's too long.
func (s *Service) SendText(dev Device, text string, ask PINAsker, prog *fileops.Progress) *fileops.Result {
	return s.send(dev, ask, prog, func(context.Context) ([]*outFile, []error) {
		id := randomID()
		f := protocol.File{ID: id, Name: id + ".txt", Size: int64(len(text)), MIME: "text/plain"}
		if len(text) <= maxMessage {
			f.Preview = &text
		}
		return []*outFile{{file: f, data: []byte(text)}}, nil
	})
}

func (s *Service) send(dev Device, ask PINAsker, prog *fileops.Progress, gather func(context.Context) ([]*outFile, []error)) *fileops.Result {
	res := &fileops.Result{}
	client, err := s.clientFor(dev)
	if err != nil {
		res.Errors = append(res.Errors, err)
		return res
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	snd := &sending{ip: dev.IP, cancel: cancel}
	s.mu.Lock()
	s.sends[snd] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sends, snd)
		s.mu.Unlock()
	}()
	var userCancelled atomic.Bool
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-t.C:
				if prog != nil && prog.Cancelled != nil && prog.Cancelled() {
					userCancelled.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	// result fills in why the transfer stopped, if it was stopped.
	result := func() *fileops.Result {
		snd.mu.Lock()
		byPeer := snd.cancelledByPeer
		snd.mu.Unlock()
		switch {
		case byPeer:
			res.Errors = append(res.Errors, fmt.Errorf("cancelled by %s", dev.Alias))
		case userCancelled.Load():
			res.Cancelled = true
		case s.ctx.Err() != nil:
			res.Errors = append(res.Errors, errors.New("LocalSend stopped"))
		}
		return res
	}

	files, errs := gather(ctx)
	res.Errors = append(res.Errors, errs...)
	defer func() {
		for _, f := range files {
			if f.temp != "" {
				os.Remove(f.temp)
			}
		}
	}()
	if ctx.Err() != nil {
		return result()
	}
	if len(files) == 0 {
		if len(res.Errors) == 0 {
			res.Errors = append(res.Errors, errNoFiles)
		}
		return res
	}
	report := func(done, total int, name string, err error) {
		if prog != nil && prog.OnItem != nil {
			prog.OnItem(done, total, name, err)
		}
	}
	report(0, len(files), "waiting for "+dev.Alias+" to accept", nil)

	peer := peerOf(dev)
	self := s.self()
	offered := make([]protocol.File, len(files))
	for i, f := range files {
		offered[i] = f.file
	}
	sess, err := prepareWithPIN(ctx, client, peer, self, offered, dev, ask)
	switch {
	case ctx.Err() != nil:
		return result()
	case errors.Is(err, protocol.ErrNothing):
		if _, isText := protocol.Message(offered); !isText {
			res.Errors = append(res.Errors, fmt.Errorf("%s wanted none of the files", dev.Alias))
		} else {
			res.Done = 1
			report(1, 1, "message", nil)
		}
		return res
	case err != nil:
		res.Errors = append(res.Errors, sendError(dev, err))
		return res
	}
	snd.mu.Lock()
	snd.sessionID = sess.ID
	snd.mu.Unlock()

	var wanted []*outFile
	var total int64
	for _, f := range files {
		if _, ok := sess.Tokens[f.file.ID]; ok {
			wanted = append(wanted, f)
			total += f.file.Size
		}
	}
	var sent atomic.Int64
	var filesDone atomic.Int64
	bytesDone := make(chan struct{})
	go func() {
		t := time.NewTicker(progressTick)
		defer t.Stop()
		for {
			done := false
			select {
			case <-bytesDone:
				done = true
			case <-t.C:
			}
			if prog != nil && prog.OnBytes != nil {
				prog.OnBytes(fileops.Bytes{Done: sent.Load(), Total: total, Files: int(filesDone.Load()), FilesTotal: len(wanted)})
			}
			if done {
				return
			}
		}
	}()
	for i, f := range wanted {
		if ctx.Err() != nil {
			break
		}
		before := sent.Load()
		err := upload(ctx, client, peer, sess, f, &sent)
		if err != nil {
			sent.Store(before)
			if ctx.Err() != nil {
				break
			}
			err = fmt.Errorf("%s: %w", path.Base(f.file.Name), sendError(dev, err))
			res.Errors = append(res.Errors, err)
		} else {
			res.Done++
		}
		filesDone.Add(1)
		report(i+1, len(wanted), path.Base(f.file.Name), err)
	}
	close(bytesDone)
	if ctx.Err() != nil {
		snd.mu.Lock()
		byPeer := snd.cancelledByPeer
		snd.mu.Unlock()
		if !byPeer {
			cctx, ccancel := context.WithTimeout(context.Background(), 3*time.Second)
			client.Cancel(cctx, peer, sess.ID)
			ccancel()
		}
	}
	return result()
}

// prepareWithPIN offers files to peer, asking the user the PIN while the
// peer wants one.
func prepareWithPIN(ctx context.Context, c protocol.Client, peer protocol.Peer, self protocol.Info, files []protocol.File, dev Device, ask PINAsker) (protocol.Session, error) {
	pin := ""
	for {
		sess, err := c.PrepareUpload(ctx, peer, self, files, pin)
		wrong := errors.Is(err, protocol.ErrWrongPIN)
		if !wrong && !errors.Is(err, protocol.ErrPINRequired) {
			return sess, err
		}
		if ask == nil {
			return sess, err
		}
		p, ok := ask(dev, wrong)
		if !ok || ctx.Err() != nil {
			return sess, errPINCancelled
		}
		pin = p
	}
}

// upload sends f, counting its bytes in sent.
func upload(ctx context.Context, c protocol.Client, peer protocol.Peer, sess protocol.Session, f *outFile, sent *atomic.Int64) error {
	r, err := f.open()
	if err != nil {
		return err
	}
	defer r.Close()
	return c.Upload(ctx, peer, sess, f.file.ID, &countingReader{r: r, n: sent}, f.file.Size)
}

type countingReader struct {
	r io.Reader
	n *atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// sendError says why dev refused a transfer, in a few words.
func sendError(dev Device, err error) error {
	switch {
	case errors.Is(err, protocol.ErrDeclined):
		return fmt.Errorf("declined by %s", dev.Alias)
	case errors.Is(err, protocol.ErrBusy):
		return fmt.Errorf("%s is busy with another transfer", dev.Alias)
	case errors.Is(err, protocol.ErrTooManyTries):
		return errors.New("too many wrong PINs")
	case errors.Is(err, protocol.ErrWrongPIN), errors.Is(err, protocol.ErrPINRequired):
		return errors.New("wrong PIN")
	case errors.Is(err, protocol.ErrCertificate):
		return fmt.Errorf("%s's certificate changed", dev.Alias)
	case errors.Is(err, protocol.ErrChecksum):
		return errors.New("damaged in transit")
	case errors.Is(err, protocol.ErrForbidden):
		return fmt.Errorf("refused by %s", dev.Alias)
	}
	return err
}

// cancelReceived stops the transfer being sent to ip whose session its
// receiver cancelled.
func (s *Service) cancelReceived(ip, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for snd := range s.sends {
		snd.mu.Lock()
		match := snd.ip == ip && snd.sessionID == sessionID
		if match {
			snd.cancelledByPeer = true
		}
		snd.mu.Unlock()
		if match {
			snd.cancel()
		}
	}
}

// collect lists the files to send for items: folders walked, a file in
// one named by its path from the folder ("Photos/2024/a.jpg").
func collect(ctx context.Context, items []fileops.Item) ([]*outFile, []error) {
	var files []*outFile
	var errs []error
	var add func(fs vfs.FileSystem, p, name string, e vfs.Entry, depth int)
	add = func(fs vfs.FileSystem, p, name string, e vfs.Entry, depth int) {
		if ctx.Err() != nil {
			return
		}
		e = resolveLink(fs, p, e)
		if e.IsDir {
			// Linked folders inside a folder are skipped: they may loop.
			if (e.IsSymlink && depth > 0) || depth >= maxWalkDepth {
				return
			}
			entries, err := fs.List(p)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
			for _, c := range entries {
				add(fs, fs.Join(p, c.Name), name+"/"+c.Name, c, depth+1)
			}
			return
		}
		if e.Mode&(os.ModeDevice|os.ModeCharDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeIrregular) != 0 {
			return // not a file to send
		}
		o := &outFile{fs: fs, path: p, file: protocol.File{
			ID: randomID(), Name: name, Size: e.Size, MIME: mimeType(name), Modified: e.ModTime,
		}}
		if e.SizeUnknown {
			if err := spool(o); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
		}
		files = append(files, o)
	}
	for _, it := range items {
		e, err := it.FS.Stat(it.Path)
		name := it.FS.Base(it.Path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		add(it.FS, it.Path, name, e, 0)
	}
	return files, errs
}

// resolveLink returns, for a link on the local disk, what it points to
// (its size, whether it's a folder), the link's own entry otherwise.
func resolveLink(fs vfs.FileSystem, p string, e vfs.Entry) vfs.Entry {
	if !e.IsSymlink && e.Mode&os.ModeSymlink == 0 {
		return e
	}
	lp, ok := fs.(vfs.LocalPath)
	if !ok {
		return e
	}
	local, ok := lp.LocalPath(p)
	if !ok {
		return e
	}
	info, err := os.Stat(local)
	if err != nil {
		return e
	}
	e.IsSymlink = true
	e.IsDir = info.IsDir()
	e.Size = info.Size()
	e.Mode = info.Mode()
	e.ModTime = info.ModTime()
	return e
}

// spool copies o to a temporary file, to know its size.
func spool(o *outFile) error {
	r, err := o.fs.Open(o.path)
	if err != nil {
		return err
	}
	defer r.Close()
	tmp, err := os.CreateTemp("", "shfm-localsend-*")
	if err != nil {
		return err
	}
	n, err := io.Copy(tmp, r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return err
	}
	o.temp, o.file.Size = tmp.Name(), n
	return nil
}

// mimeType guesses a file's type from its name.
func mimeType(name string) string {
	ext := path.Ext(name)
	if t := mime.TypeByExtension(strings.ToLower(ext)); t != "" {
		if mt, _, err := mime.ParseMediaType(t); err == nil {
			return mt
		}
	}
	return "application/octet-stream"
}

func randomID() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
