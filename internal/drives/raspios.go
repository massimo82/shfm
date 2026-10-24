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

package drives

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"time"
)

// --- Raspberry Pi OS images: their boot partition, a plain FAT one ----------

// IsRaspiOSImage reports whether the image read by r is Raspberry Pi OS
// (formerly Raspbian), or built as it is: its boot partition, a FAT one,
// holds the issue.txt pi-gen writes there (a copy of /etc/rpi-issue:
// "Raspberry Pi reference <date>", then "Generated using pi-gen"), or,
// for the older images without it, the image is named after it.
// Raspberry Pi Imager itself trusts its online list of images instead,
// offering nothing for a file of the user's own.
func IsRaspiOSImage(name string, r io.ReaderAt) bool {
	v, err := findBootFAT(r)
	if err != nil {
		return false
	}
	if e, ok, err := v.findRoot("ISSUE   TXT"); err == nil && ok {
		head, err := v.readFile(e, 512)
		if err == nil && (bytes.HasPrefix(head, []byte("Raspberry Pi reference")) ||
			bytes.Contains(head, []byte("Generated using pi-gen"))) {
			return true
		}
	}
	lower := strings.ToLower(name)
	return strings.Contains(lower, "raspios") || strings.Contains(lower, "raspbian")
}

// RaspiSetup is what SetupRaspiOS does to the Raspberry Pi OS written.
type RaspiSetup struct {
	SSH  bool       // turn SSH on
	User *RaspiUser // create this user; nil: the first boot asks for one
}

// Empty reports whether s does nothing.
func (s RaspiSetup) Empty() bool { return !s.SSH && s.User == nil }

// RaspiUser is the user the first boot of Raspberry Pi OS creates, in
// place of asking for one: its name and its password, hashed.
type RaspiUser struct {
	Name         string
	PasswordHash string // crypt(3) format, as chpasswd -e takes it
}

// NewRaspiUser checks name and password as Raspberry Pi OS does (its
// userconf-pi): the name 1 to 32 lower-case letters, digits and hyphens,
// starting with a letter, not root; the password not empty. The password
// is kept hashed only (SHA-512 crypt, which every release reads).
func NewRaspiUser(name, password string) (RaspiUser, error) {
	switch {
	case name == "" || len(name) > 32:
		return RaspiUser{}, errors.New("user name: 1 to 32 characters")
	case name == "root":
		return RaspiUser{}, errors.New("user name can't be root")
	case !validRaspiUserName(name):
		return RaspiUser{}, errors.New("user name: a-z first, then a-z 0-9 -")
	case password == "":
		return RaspiUser{}, errors.New("password can't be empty")
	}
	hash, err := sha512Crypt(password)
	if err != nil {
		return RaspiUser{}, err
	}
	return RaspiUser{Name: name, PasswordHash: hash}, nil
}

func validRaspiUserName(name string) bool {
	for i, c := range name {
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// SetupRaspiOS sets up the Raspberry Pi OS written on dev, through files
// in its boot partition (/boot/firmware, or /boot on the older ones) that
// its first boot reads and removes:
//   - ssh, empty, turns the SSH server on for good; nothing is done when
//     it (or ssh.txt) is there already;
//   - userconf.txt, "name:password hash", creates the user (renaming the
//     image's own first one) without asking for it; one there already,
//     userconf or userconf.txt, is replaced.
//
// Written straight into the FAT filesystem, without mounting it.
func SetupRaspiOS(dev interface {
	io.ReaderAt
	io.WriterAt
}, setup RaspiSetup, now time.Time) error {
	v, err := findBootFAT(dev)
	if err != nil {
		return err
	}
	v.w = dev
	if setup.User != nil {
		// userconf-pi reads userconf first, should both be there.
		name := "USERCONFTXT"
		if _, ok, err := v.findRoot("USERCONF   "); err != nil {
			return err
		} else if ok {
			name = "USERCONF   "
		}
		line := setup.User.Name + ":" + setup.User.PasswordHash + "\n"
		if err := v.writeRootFile(name, []byte(line), now); err != nil {
			return err
		}
	}
	if !setup.SSH {
		return nil
	}
	for _, name := range []string{"SSH        ", "SSH     TXT"} {
		if _, ok, err := v.findRoot(name); err != nil || ok {
			return err
		}
	}
	return v.writeRootFile("SSH        ", nil, now)
}

var errNoBootFAT = errors.New("no FAT boot partition")

const sector = 512 // what partition tables count in, on images

// findBootFAT returns the image's first FAT partition, listed by its MBR
// or, behind a protective one, its GPT.
func findBootFAT(r io.ReaderAt) (*fatVolume, error) {
	var mbr [sector]byte
	if _, err := r.ReadAt(mbr[:], 0); err != nil {
		return nil, errNoBootFAT
	}
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		return nil, errNoBootFAT
	}
	var starts []int64
	for i := range 4 {
		p := mbr[446+16*i:]
		kind, lba := p[4], binary.LittleEndian.Uint32(p[8:])
		switch {
		case kind == 0xEE:
			return findBootFATInGPT(r)
		case kind != 0 && lba != 0:
			starts = append(starts, int64(lba)*sector)
		}
	}
	return firstFAT(r, starts)
}

func findBootFATInGPT(r io.ReaderAt) (*fatVolume, error) {
	var hdr [92]byte
	if _, err := r.ReadAt(hdr[:], sector); err != nil || string(hdr[:8]) != "EFI PART" {
		return nil, errNoBootFAT
	}
	lba := binary.LittleEndian.Uint64(hdr[72:])
	count := min(binary.LittleEndian.Uint32(hdr[80:]), 128)
	size := binary.LittleEndian.Uint32(hdr[84:])
	if size < 128 || size > 4096 {
		return nil, errNoBootFAT
	}
	var starts []int64
	entry := make([]byte, size)
	for i := range int64(count) {
		if _, err := r.ReadAt(entry, int64(lba)*sector+i*int64(size)); err != nil {
			break
		}
		if first := binary.LittleEndian.Uint64(entry[32:]); first != 0 && !allZero(entry[:16]) {
			starts = append(starts, int64(first)*sector)
		}
	}
	return firstFAT(r, starts)
}

func firstFAT(r io.ReaderAt, starts []int64) (*fatVolume, error) {
	for _, s := range starts {
		if v, err := openFAT(r, s); err == nil {
			return v, nil
		}
	}
	return nil, errNoBootFAT
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// fatVolume is a FAT12, FAT16 or FAT32 filesystem starting at base.
type fatVolume struct {
	r    io.ReaderAt
	w    io.WriterAt // nil: read only
	base int64

	bits        int // 12, 16 or 32
	bytesPerSec int64
	clusterSize int64
	fatStart    int64 // first FAT, from base
	fatSize     int64 // bytes per FAT
	numFATs     int
	rootStart   int64 // FAT12/16: the fixed root folder, from base
	rootSize    int64
	rootCluster uint32 // FAT32: the root folder's first cluster
	dataStart   int64  // cluster 2, from base
	clusters    uint32 // data clusters: they're numbered 2 to clusters+1
	fsInfo      int64  // FAT32: its FSInfo sector, from base; 0 when none
}

// openFAT reads the boot sector at base, refusing what isn't FAT.
func openFAT(r io.ReaderAt, base int64) (*fatVolume, error) {
	var bs [sector]byte
	if _, err := r.ReadAt(bs[:], base); err != nil {
		return nil, err
	}
	le16 := func(o int) int64 { return int64(binary.LittleEndian.Uint16(bs[o:])) }
	le32 := func(o int) int64 { return int64(binary.LittleEndian.Uint32(bs[o:])) }
	bps, spc := le16(11), int64(bs[13])
	reserved, fats, rootEntries := le16(14), int64(bs[16]), le16(17)
	total, fatSecs := le16(19), le16(22)
	if total == 0 {
		total = le32(32)
	}
	if fatSecs == 0 {
		fatSecs = le32(36)
	}
	pow2 := func(n int64) bool { return n > 0 && n&(n-1) == 0 }
	if bs[510] != 0x55 || bs[511] != 0xAA || !pow2(bps) || bps < 512 || bps > 4096 ||
		!pow2(spc) || reserved == 0 || fats == 0 || fats > 4 || fatSecs == 0 {
		return nil, errNoBootFAT
	}
	rootSecs := (rootEntries*32 + bps - 1) / bps
	dataSec := reserved + fats*fatSecs + rootSecs
	if total <= dataSec {
		return nil, errNoBootFAT
	}
	v := &fatVolume{
		r: r, base: base,
		bytesPerSec: bps,
		clusterSize: bps * spc,
		fatStart:    reserved * bps,
		fatSize:     fatSecs * bps,
		numFATs:     int(fats),
		rootStart:   (reserved + fats*fatSecs) * bps,
		rootSize:    rootSecs * bps,
		dataStart:   dataSec * bps,
		clusters:    uint32((total - dataSec) / spc),
	}
	switch {
	case v.clusters < 4085:
		v.bits = 12
	case v.clusters < 65525:
		v.bits = 16
	default:
		v.bits = 32
		v.rootCluster = uint32(le32(44))
		if fsi := le16(48); fsi != 0 && fsi != 0xFFFF {
			v.fsInfo = fsi * bps
		}
		if rootEntries != 0 || v.rootCluster < 2 {
			return nil, errNoBootFAT
		}
	}
	if v.fatSize*8 < int64(v.clusters+2)*int64(v.bits) {
		return nil, errNoBootFAT // a FAT too small for its clusters
	}
	return v, nil
}

func (v *fatVolume) clusterOffset(c uint32) int64 {
	return v.dataStart + int64(c-2)*v.clusterSize
}

func (v *fatVolume) validCluster(c uint32) bool { return c >= 2 && c < v.clusters+2 }

// next is the cluster after c in its chain; ok false at its end.
func (v *fatVolume) next(c uint32) (n uint32, ok bool, err error) {
	var b [4]byte
	switch v.bits {
	case 12:
		off := int64(c) + int64(c)/2
		if _, err := v.r.ReadAt(b[:2], v.base+v.fatStart+off); err != nil {
			return 0, false, err
		}
		n = uint32(binary.LittleEndian.Uint16(b[:]))
		if c&1 == 1 {
			n >>= 4
		}
		n &= 0xFFF
	case 16:
		if _, err := v.r.ReadAt(b[:2], v.base+v.fatStart+int64(c)*2); err != nil {
			return 0, false, err
		}
		n = uint32(binary.LittleEndian.Uint16(b[:]))
	default:
		if _, err := v.r.ReadAt(b[:], v.base+v.fatStart+int64(c)*4); err != nil {
			return 0, false, err
		}
		n = binary.LittleEndian.Uint32(b[:]) & 0x0FFFFFFF
	}
	return n, v.validCluster(n), nil
}

// rootChunks are where the root folder lies: its fixed area, or the
// clusters of its chain.
func (v *fatVolume) rootChunks() ([]int64, int64, error) {
	if v.bits != 32 {
		return []int64{v.rootStart}, v.rootSize, nil
	}
	var offs []int64
	c := v.rootCluster
	for range v.clusters {
		if !v.validCluster(c) {
			return nil, 0, errors.New("damaged FAT boot partition")
		}
		offs = append(offs, v.clusterOffset(c))
		n, ok, err := v.next(c)
		if err != nil || !ok {
			return offs, v.clusterSize, err
		}
		c = n
	}
	return nil, 0, errors.New("damaged FAT boot partition") // a loop
}

// dirSlot is a root folder entry: its 32 bytes and where they are.
type dirSlot struct {
	off   int64 // from base
	entry [32]byte
}

// eachRoot calls fn on every slot of the root folder, used or free, until
// it returns true or the end mark (a slot starting with 0) has been seen.
func (v *fatVolume) eachRoot(fn func(dirSlot) bool) error {
	offs, size, err := v.rootChunks()
	if err != nil {
		return err
	}
	buf := make([]byte, size)
	for _, off := range offs {
		if _, err := v.r.ReadAt(buf, v.base+off); err != nil {
			return err
		}
		for i := int64(0); i+32 <= size; i += 32 {
			s := dirSlot{off: off + i}
			copy(s.entry[:], buf[i:])
			if fn(s) || s.entry[0] == 0 {
				return nil
			}
		}
	}
	return nil
}

// findRoot looks in the root folder for the file whose 8.3 name, padded
// with spaces as stored, is name.
func (v *fatVolume) findRoot(name string) (e [32]byte, found bool, err error) {
	s, found, err := v.findRootSlot(name)
	return s.entry, found, err
}

func (v *fatVolume) findRootSlot(name string) (slot dirSlot, found bool, err error) {
	err = v.eachRoot(func(s dirSlot) bool {
		attr := s.entry[11]
		if s.entry[0] == 0 || s.entry[0] == 0xE5 || attr&0x0F == 0x0F || attr&0x18 != 0 {
			return false // free, long name part, volume label or folder
		}
		if string(s.entry[:11]) == name {
			slot, found = s, true
		}
		return found
	})
	return slot, found, err
}

// entryCluster is the first cluster of the file of entry e.
func (v *fatVolume) entryCluster(e [32]byte) uint32 {
	c := uint32(binary.LittleEndian.Uint16(e[26:]))
	if v.bits == 32 {
		c |= uint32(binary.LittleEndian.Uint16(e[20:])) << 16 // reserved elsewhere
	}
	return c
}

// readFile returns the first max bytes at most of the file of entry e.
func (v *fatVolume) readFile(e [32]byte, max int64) ([]byte, error) {
	size := min(int64(binary.LittleEndian.Uint32(e[28:])), max)
	c := v.entryCluster(e)
	out := make([]byte, 0, size)
	for int64(len(out)) < size {
		if !v.validCluster(c) {
			return nil, errors.New("damaged FAT boot partition")
		}
		chunk := make([]byte, min(v.clusterSize, size-int64(len(out))))
		if _, err := v.r.ReadAt(chunk, v.base+v.clusterOffset(c)); err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		n, ok, err := v.next(c)
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		c = n
	}
	return out, nil
}

// writeRootFile makes the root folder's file name (8.3, padded) hold
// data, its times now: the file there is rewritten, in new clusters, its
// old ones freed; else it's added, shown in lower case. A FAT32 root
// folder that's full grows by a cluster.
func (v *fatVolume) writeRootFile(name string, data []byte, now time.Time) error {
	old, exists, err := v.findRootSlot(name)
	if err != nil {
		return err
	}
	var slot int64 = -1
	if exists {
		slot = old.off
	} else if err := v.eachRoot(func(s dirSlot) bool {
		if s.entry[0] == 0 || s.entry[0] == 0xE5 {
			slot = s.off
		}
		return slot >= 0
	}); err != nil {
		return err
	}
	if slot < 0 && v.bits != 32 {
		return errors.New("boot partition's root folder full")
	}
	// The data first, then the entry pointing to it.
	var first uint32
	if len(data) > 0 {
		chain, err := v.allocChain(int((int64(len(data)) + v.clusterSize - 1) / v.clusterSize))
		if err != nil {
			return err
		}
		for i, c := range chain {
			chunk := make([]byte, v.clusterSize)
			copy(chunk, data[min(int64(len(data)), int64(i)*v.clusterSize):])
			if _, err := v.w.WriteAt(chunk, v.base+v.clusterOffset(c)); err != nil {
				return err
			}
		}
		first = chain[0]
	}
	if slot < 0 {
		if slot, err = v.growRoot(); err != nil {
			return err
		}
	}
	e := old.entry
	if !exists {
		e = [32]byte{}
		copy(e[:11], name)
		e[11] = 0x20 // archive
		// Shown in lower case, as Windows and Linux store such names.
		if name[:8] != "        " {
			e[12] |= 0x08
		}
		if name[8:] != "   " {
			e[12] |= 0x10
		}
	}
	date, tm := fatTime(now)
	if !exists {
		binary.LittleEndian.PutUint16(e[14:], tm)
		binary.LittleEndian.PutUint16(e[16:], date)
	}
	binary.LittleEndian.PutUint16(e[18:], date)
	binary.LittleEndian.PutUint16(e[20:], uint16(first>>16))
	binary.LittleEndian.PutUint16(e[22:], tm)
	binary.LittleEndian.PutUint16(e[24:], date)
	binary.LittleEndian.PutUint16(e[26:], uint16(first))
	binary.LittleEndian.PutUint32(e[28:], uint32(len(data)))
	if _, err := v.w.WriteAt(e[:], v.base+slot); err != nil {
		return err
	}
	if exists {
		return v.freeChain(v.entryCluster(old.entry))
	}
	return nil
}

// fatTime is t as FAT stores it: date and time, to two seconds.
func fatTime(t time.Time) (date, tm uint16) {
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, t.Location())
	}
	date = uint16((t.Year()-1980)<<9 | int(t.Month())<<5 | t.Day())
	tm = uint16(t.Hour()<<11 | t.Minute()<<5 | t.Second()/2)
	return date, tm
}

// growRoot adds a free cluster, emptied, at the end of the FAT32 root
// folder's chain, and returns where it starts.
func (v *fatVolume) growRoot() (int64, error) {
	offs, _, err := v.rootChunks()
	if err != nil {
		return 0, err
	}
	last := uint32((offs[len(offs)-1]-v.dataStart)/v.clusterSize) + 2
	chain, err := v.allocChain(1)
	if err != nil {
		return 0, err
	}
	if _, err := v.w.WriteAt(make([]byte, v.clusterSize), v.base+v.clusterOffset(chain[0])); err != nil {
		return 0, err
	}
	if err := v.setFAT(last, chain[0]); err != nil {
		return 0, err
	}
	return v.clusterOffset(chain[0]), nil
}

// endOfChain is the FAT entry closing a chain, whatever its width.
const endOfChain = 0x0FFFFFFF

// allocChain takes n free clusters, chained in that order, the last
// closing the chain.
func (v *fatVolume) allocChain(n int) ([]uint32, error) {
	var chain []uint32
	for c := uint32(2); c < v.clusters+2 && len(chain) < n; c++ {
		next, _, err := v.next(c)
		if err != nil {
			return nil, err
		}
		if next == 0 {
			chain = append(chain, c)
		}
	}
	if len(chain) < n {
		return nil, errors.New("boot partition full")
	}
	for i, c := range chain {
		next := uint32(endOfChain)
		if i+1 < len(chain) {
			next = chain[i+1]
		}
		if err := v.setFAT(c, next); err != nil {
			return nil, err
		}
	}
	v.updateFSInfo(-n, chain[n-1]+1)
	return chain, nil
}

// freeChain frees the clusters of the chain starting at first.
func (v *fatVolume) freeChain(first uint32) error {
	freed := 0
	for c := first; v.validCluster(c) && freed < int(v.clusters); {
		n, ok, err := v.next(c)
		if err != nil {
			return err
		}
		if err := v.setFAT(c, 0); err != nil {
			return err
		}
		freed++
		if !ok {
			break
		}
		c = n
	}
	v.updateFSInfo(freed, 0)
	return nil
}

// setFAT makes cluster c point to n, in every FAT, keeping what the
// entry's other bits hold: FAT32's top four, reserved, and FAT12's
// neighbour.
func (v *fatVolume) setFAT(c, n uint32) error {
	var b [4]byte
	for i := range int64(v.numFATs) {
		fat := v.base + v.fatStart + i*v.fatSize
		var off int64
		var size int
		switch v.bits {
		case 12:
			off, size = int64(c)+int64(c)/2, 2
		case 16:
			off, size = int64(c)*2, 2
		default:
			off, size = int64(c)*4, 4
		}
		if _, err := v.r.ReadAt(b[:size], fat+off); err != nil {
			return err
		}
		switch v.bits {
		case 12:
			old := binary.LittleEndian.Uint16(b[:])
			if c&1 == 1 {
				binary.LittleEndian.PutUint16(b[:], old&0x000F|uint16(n&0xFFF)<<4)
			} else {
				binary.LittleEndian.PutUint16(b[:], old&0xF000|uint16(n&0xFFF))
			}
		case 16:
			binary.LittleEndian.PutUint16(b[:], uint16(n))
		default:
			binary.LittleEndian.PutUint32(b[:], binary.LittleEndian.Uint32(b[:])&0xF0000000|n&0x0FFFFFFF)
		}
		if _, err := v.w.WriteAt(b[:size], fat+off); err != nil {
			return err
		}
	}
	return nil
}

// updateFSInfo counts delta more free clusters in FAT32's FSInfo hints,
// pointing the next free one at hint (0: left as it is). Only hints: a
// failure leaves them be.
func (v *fatVolume) updateFSInfo(delta int, hint uint32) {
	if v.bits != 32 || v.fsInfo == 0 {
		return
	}
	var fsi [sector]byte
	if _, err := v.r.ReadAt(fsi[:], v.base+v.fsInfo); err != nil ||
		binary.LittleEndian.Uint32(fsi[0:]) != 0x41615252 || binary.LittleEndian.Uint32(fsi[484:]) != 0x61417272 {
		return
	}
	if n := binary.LittleEndian.Uint32(fsi[488:]); n != 0xFFFFFFFF && int64(n)+int64(delta) >= 0 {
		binary.LittleEndian.PutUint32(fsi[488:], uint32(int64(n)+int64(delta)))
	}
	if hint != 0 {
		binary.LittleEndian.PutUint32(fsi[492:], hint)
	}
	v.w.WriteAt(fsi[484:500], v.base+v.fsInfo+484)
}
