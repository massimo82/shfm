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

// Package wlclip reads and writes the Wayland clipboard (the "selection")
// without any external tool: a minimal Wayland client, speaking the wire
// protocol directly over the compositor's Unix socket, that uses the
// data-control extension — ext-data-control-v1, or the older
// zwlr-data-control-v1 it was standardized from (identical messages).
// Data-control exists precisely for clients without a window, like a
// terminal program: the core wl_data_device protocol would need a focused
// surface. Compositors that don't offer it (e.g. GNOME's, as of this
// writing) make Connect fail, and callers simply go without.
//
// Only what a clipboard needs is implemented: the registry, wl_seat (to
// create the data device), and the data-control manager, device, source
// and offer interfaces.
package wlclip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// object kinds the client tracks.
type kind int

const (
	kDisplay kind = iota
	kRegistry
	kCallback
	kSeat
	kManager
	kDevice
	kSource
	kOffer
)

// Opcodes. The data-control interfaces are the same in both protocol
// flavours.
const (
	displaySync        = 0
	displayGetRegistry = 1
	registryBind       = 0

	managerCreateSource = 0
	managerGetDevice    = 1
	deviceSetSelection  = 0
	sourceOffer         = 0
	sourceDestroy       = 1
	offerReceive        = 0
	offerDestroy        = 1

	evDisplayError   = 0
	evDisplayDelete  = 1
	evRegistryGlobal = 0
	evCallbackDone   = 0
	evDeviceOffer    = 0
	evDeviceSel      = 1
	evDeviceFinished = 2
	evDevicePrimary  = 3
	evSourceSend     = 0
	evSourceCancel   = 1
	evOfferMime      = 0
)

// ErrUnavailable means there is no Wayland session, or its compositor
// doesn't support data-control.
var ErrUnavailable = errors.New("wlclip: no Wayland data-control support")

// Client is a connection to the compositor's clipboard.
type Client struct {
	conn *net.UnixConn
	wmu  sync.Mutex // serializes writes

	mu        sync.Mutex
	nextID    uint32
	objs      map[uint32]kind
	globals   map[string]global
	offers    map[uint32][]string // offer id → MIME types offered
	selection uint32              // current selection offer (0 = none)
	primary   uint32              // current primary-selection offer, only to destroy it
	device    uint32
	manager   uint32
	source    uint32            // our selection source, 0 when not owning it
	data      map[string][]byte // what our source serves, by MIME type
	syncs     map[uint32]chan struct{}
	onChange  func()
	err       error

	fds []int // received file descriptors not yet consumed
}

type global struct {
	name    uint32
	version uint32
}

// Connect opens the Wayland display named by $WAYLAND_DISPLAY (relative
// to $XDG_RUNTIME_DIR) and sets up a data-control device on its first
// seat. It fails with an error wrapping ErrUnavailable when there's no
// Wayland session or no data-control support.
func Connect() (*Client, error) {
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		return nil, fmt.Errorf("%w: WAYLAND_DISPLAY not set", ErrUnavailable)
	}
	path := name
	if !filepath.IsAbs(path) {
		dir := os.Getenv("XDG_RUNTIME_DIR")
		if dir == "" {
			return nil, fmt.Errorf("%w: XDG_RUNTIME_DIR not set", ErrUnavailable)
		}
		path = filepath.Join(dir, name)
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	c := &Client{
		conn:    conn,
		nextID:  2,
		objs:    map[uint32]kind{1: kDisplay},
		globals: map[string]global{},
		offers:  map[uint32][]string{},
		syncs:   map[uint32]chan struct{}{},
	}
	go c.readLoop()
	if err := c.setup(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) setup() error {
	registry := c.newID(kRegistry)
	if err := c.send(1, displayGetRegistry, nil, u32(registry)); err != nil {
		return err
	}
	if err := c.roundtrip(); err != nil {
		return err
	}

	c.mu.Lock()
	seat, hasSeat := c.globals["wl_seat"]
	var mgrName string
	var mgr global
	hasMgr := false
	for _, name := range []string{"ext_data_control_manager_v1", "zwlr_data_control_manager_v1"} {
		if g, ok := c.globals[name]; ok {
			mgrName, mgr, hasMgr = name, g, true
			break
		}
	}
	c.mu.Unlock()
	if !hasSeat || !hasMgr {
		return fmt.Errorf("%w: compositor lacks wl_seat or data-control", ErrUnavailable)
	}

	seatID := c.newID(kSeat)
	if err := c.send(registry, registryBind, nil, u32(seat.name), str("wl_seat"), u32(1), u32(seatID)); err != nil {
		return err
	}
	c.manager = c.newID(kManager)
	if err := c.send(registry, registryBind, nil, u32(mgr.name), str(mgrName), u32(1), u32(c.manager)); err != nil {
		return err
	}
	c.device = c.newID(kDevice)
	if err := c.send(c.manager, managerGetDevice, nil, u32(c.device), u32(seatID)); err != nil {
		return err
	}
	// The device reports the current selection right away.
	return c.roundtrip()
}

// OnChange registers f, called (from the client's own goroutine) whenever
// the selection changes — including when this client takes it.
func (c *Client) OnChange(f func()) {
	c.mu.Lock()
	c.onChange = f
	c.mu.Unlock()
}

// Selection reports the MIME types the current selection offers, and
// whether it is this client's own.
func (c *Client) Selection() (mimes []string, owned bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.offers[c.selection]...), c.source != 0
}

// SetSelection takes the clipboard, offering data under each of its MIME
// types. The data is served for as long as this client owns the
// selection (until another client copies something, or Close).
func (c *Client) SetSelection(data map[string][]byte) error {
	src := c.newID(kSource)
	if err := c.send(c.manager, managerCreateSource, nil, u32(src)); err != nil {
		return err
	}
	for mime := range data {
		if err := c.send(src, sourceOffer, nil, str(mime)); err != nil {
			return err
		}
	}
	c.mu.Lock()
	old := c.source
	c.source, c.data = src, data
	c.mu.Unlock()
	if err := c.send(c.device, deviceSetSelection, nil, u32(src)); err != nil {
		return err
	}
	if old != 0 {
		c.destroy(old, sourceDestroy)
	}
	return nil
}

// Receive reads the current selection's content as mime, waiting at most
// timeout for the owning client to write it.
func (c *Client) Receive(mime string, timeout time.Duration) ([]byte, error) {
	c.mu.Lock()
	offer := c.selection
	c.mu.Unlock()
	if offer == 0 {
		return nil, errors.New("wlclip: clipboard is empty")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	err = c.send(offer, offerReceive, []int{int(w.Fd())}, str(mime))
	w.Close() // the owner holds its own copy now
	if err != nil {
		return nil, err
	}
	r.SetReadDeadline(time.Now().Add(timeout))
	return io.ReadAll(r)
}

// Close disconnects; a selection this client owned goes away with it.
func (c *Client) Close() error {
	return c.conn.Close()
}

// --- wire protocol -------------------------------------------------------------

func (c *Client) newID(k kind) uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	c.objs[id] = k
	return id
}

func (c *Client) destroy(id uint32, opcode uint16) {
	c.send(id, opcode, nil)
	c.mu.Lock()
	delete(c.objs, id)
	delete(c.offers, id)
	c.mu.Unlock()
}

// roundtrip waits until the compositor has processed every request sent
// so far (and we've handled the events they caused).
func (c *Client) roundtrip() error {
	cb := c.newID(kCallback)
	done := make(chan struct{})
	c.mu.Lock()
	c.syncs[cb] = done
	c.mu.Unlock()
	if err := c.send(1, displaySync, nil, u32(cb)); err != nil {
		return err
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		return fmt.Errorf("%w: compositor not responding", ErrUnavailable)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// arg is one encoded message argument.
type arg []byte

func u32(v uint32) arg {
	return binary.LittleEndian.AppendUint32(nil, v)
}

// str encodes a string: length including the NUL, bytes, NUL, padding.
func str(s string) arg {
	b := binary.LittleEndian.AppendUint32(nil, uint32(len(s)+1))
	b = append(b, s...)
	b = append(b, 0)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func (c *Client) send(obj uint32, opcode uint16, fds []int, args ...arg) error {
	size := 8
	for _, a := range args {
		size += len(a)
	}
	msg := make([]byte, 0, size)
	msg = binary.LittleEndian.AppendUint32(msg, obj)
	msg = binary.LittleEndian.AppendUint32(msg, uint32(size)<<16|uint32(opcode))
	for _, a := range args {
		msg = append(msg, a...)
	}
	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, _, err := c.conn.WriteMsgUnix(msg, oob, nil)
	return err
}

func (c *Client) readLoop() {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	oob := make([]byte, syscall.CmsgSpace(28*4))
	for {
		n, oobn, _, _, err := c.conn.ReadMsgUnix(chunk, oob)
		if err != nil {
			c.fail(err)
			return
		}
		if oobn > 0 {
			if msgs, err := syscall.ParseSocketControlMessage(oob[:oobn]); err == nil {
				for _, m := range msgs {
					if fds, err := syscall.ParseUnixRights(&m); err == nil {
						c.fds = append(c.fds, fds...)
					}
				}
			}
		}
		buf = append(buf, chunk[:n]...)
		for len(buf) >= 8 {
			obj := binary.LittleEndian.Uint32(buf[0:4])
			word := binary.LittleEndian.Uint32(buf[4:8])
			size, opcode := int(word>>16), uint16(word&0xffff)
			if size < 8 {
				c.fail(errors.New("wlclip: malformed message"))
				return
			}
			if len(buf) < size {
				break
			}
			c.handle(obj, opcode, buf[8:size])
			buf = buf[size:]
		}
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	for id, ch := range c.syncs {
		close(ch)
		delete(c.syncs, id)
	}
	c.mu.Unlock()
}

// reader decodes event arguments.
type reader struct{ b []byte }

func (r *reader) u32() uint32 {
	if len(r.b) < 4 {
		return 0
	}
	v := binary.LittleEndian.Uint32(r.b)
	r.b = r.b[4:]
	return v
}

func (r *reader) str() string {
	n := int(r.u32())
	if n == 0 || n > len(r.b) {
		return ""
	}
	s := string(r.b[:n-1])
	padded := (n + 3) &^ 3
	if padded > len(r.b) {
		padded = len(r.b)
	}
	r.b = r.b[padded:]
	return s
}

func (c *Client) popFD() int {
	if len(c.fds) == 0 {
		return -1
	}
	fd := c.fds[0]
	c.fds = c.fds[1:]
	return fd
}

func (c *Client) handle(obj uint32, opcode uint16, body []byte) {
	r := &reader{body}
	c.mu.Lock()
	k, known := c.objs[obj]
	if !known {
		c.mu.Unlock()
		return
	}
	var notify func()
	switch {
	case k == kDisplay && opcode == evDisplayError:
		r.u32()
		code := r.u32()
		c.mu.Unlock()
		c.fail(fmt.Errorf("wlclip: protocol error %d: %s", code, r.str()))
		return
	case k == kDisplay && opcode == evDisplayDelete:
		delete(c.objs, r.u32())
	case k == kRegistry && opcode == evRegistryGlobal:
		name := r.u32()
		iface := r.str()
		version := r.u32()
		if _, dup := c.globals[iface]; !dup {
			c.globals[iface] = global{name: name, version: version}
		}
	case k == kCallback && opcode == evCallbackDone:
		if ch, ok := c.syncs[obj]; ok {
			close(ch)
			delete(c.syncs, obj)
		}
		delete(c.objs, obj)
	case k == kDevice && opcode == evDeviceOffer:
		id := r.u32()
		c.objs[id] = kOffer
		c.offers[id] = nil
	case k == kDevice && opcode == evDeviceSel:
		id := r.u32()
		old := c.selection
		c.selection = id
		if old != 0 && old != id && old != c.primary {
			defer c.destroy(old, offerDestroy)
		}
		notify = c.onChange
	case k == kDevice && opcode == evDevicePrimary:
		id := r.u32()
		old := c.primary
		c.primary = id
		if old != 0 && old != id && old != c.selection {
			defer c.destroy(old, offerDestroy)
		}
	case k == kDevice && opcode == evDeviceFinished:
		c.mu.Unlock()
		c.fail(errors.New("wlclip: data device finished"))
		return
	case k == kOffer && opcode == evOfferMime:
		c.offers[obj] = append(c.offers[obj], r.str())
	case k == kSource && opcode == evSourceSend:
		mime := r.str()
		fd := c.popFD()
		var data []byte
		if obj == c.source {
			data = c.data[mime]
		}
		if fd >= 0 {
			go func() {
				f := os.NewFile(uintptr(fd), "wlclip-send")
				f.Write(data)
				f.Close()
			}()
		}
	case k == kSource && opcode == evSourceCancel:
		if obj == c.source {
			c.source, c.data = 0, nil
		}
		defer c.destroy(obj, sourceDestroy)
	}
	c.mu.Unlock()
	if notify != nil {
		notify()
	}
}
