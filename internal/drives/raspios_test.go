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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// memDisk is an image held in memory, readable and writable at offsets.
type memDisk []byte

func (d memDisk) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(d)) {
		return 0, fmt.Errorf("read past the end")
	}
	n := copy(p, d[off:])
	if n < len(p) {
		return n, fmt.Errorf("short read")
	}
	return n, nil
}

func (d memDisk) WriteAt(p []byte, off int64) (int, error) {
	if off+int64(len(p)) > int64(len(d)) {
		return 0, fmt.Errorf("write past the end")
	}
	return copy(d[off:], p), nil
}

const testPartLBA = 8 // where the test images' FAT partition starts

// fatSpec describes a synthetic image: an MBR, then one FAT partition.
type fatSpec struct {
	bits      int    // 12, 16, 32
	gpt       bool   // a protective MBR and a GPT listing the partition
	issue     string // issue.txt's content; "" for none
	rootFull  bool   // no free slot left in the root folder
	extraRoot []string
}

// buildFATImage formats, as mkfs.fat would, a FAT filesystem of 512-byte
// sectors and clusters, enough of them to be of spec.bits.
func buildFATImage(t *testing.T, spec fatSpec) memDisk {
	t.Helper()
	clusters := map[int]int64{12: 2000, 16: 5000, 32: 66000}[spec.bits]
	var reserved, rootEntries int64 = 1, 64
	if spec.bits == 32 {
		reserved, rootEntries = 32, 0
	}
	fatSecs := ((clusters+2)*int64(spec.bits)/8 + 511) / 512
	rootSecs := rootEntries * 32 / 512
	total := reserved + 2*fatSecs + rootSecs + clusters
	disk := make(memDisk, (testPartLBA+total+33)*512)
	base := int64(testPartLBA * 512)

	if spec.gpt {
		disk[446+4] = 0xEE
		binary.LittleEndian.PutUint32(disk[446+8:], 1)
		copy(disk[512:], "EFI PART")
		binary.LittleEndian.PutUint64(disk[512+72:], 2)
		binary.LittleEndian.PutUint32(disk[512+80:], 4)
		binary.LittleEndian.PutUint32(disk[512+84:], 128)
		e := disk[2*512:]
		copy(e, []byte{0xA2, 0xA0, 0xD0, 0xEB}) // basic data, enough to be "used"
		binary.LittleEndian.PutUint64(e[32:], testPartLBA)
		binary.LittleEndian.PutUint64(e[40:], uint64(testPartLBA+total-1))
	} else {
		disk[446+4] = 0x0C
		binary.LittleEndian.PutUint32(disk[446+8:], testPartLBA)
		binary.LittleEndian.PutUint32(disk[446+12:], uint32(total))
	}
	disk[510], disk[511] = 0x55, 0xAA

	bs := disk[base:]
	bs[0], bs[1], bs[2] = 0xEB, 0x58, 0x90
	binary.LittleEndian.PutUint16(bs[11:], 512)
	bs[13] = 1
	binary.LittleEndian.PutUint16(bs[14:], uint16(reserved))
	bs[16] = 2
	binary.LittleEndian.PutUint16(bs[17:], uint16(rootEntries))
	bs[21] = 0xF8
	if total < 0x10000 {
		binary.LittleEndian.PutUint16(bs[19:], uint16(total))
	} else {
		binary.LittleEndian.PutUint32(bs[32:], uint32(total))
	}
	if spec.bits == 32 {
		binary.LittleEndian.PutUint32(bs[36:], uint32(fatSecs))
		binary.LittleEndian.PutUint32(bs[44:], 2)
		binary.LittleEndian.PutUint16(bs[48:], 1)
		fsi := bs[512:]
		binary.LittleEndian.PutUint32(fsi[0:], 0x41615252)
		binary.LittleEndian.PutUint32(fsi[484:], 0x61417272)
		binary.LittleEndian.PutUint32(fsi[488:], uint32(clusters-2))
		binary.LittleEndian.PutUint32(fsi[492:], 4)
		binary.LittleEndian.PutUint32(fsi[508:], 0xAA550000)
	} else {
		binary.LittleEndian.PutUint16(bs[22:], uint16(fatSecs))
	}
	bs[510], bs[511] = 0x55, 0xAA

	v, err := openFAT(disk, base)
	if err != nil || v.bits != spec.bits {
		t.Fatalf("test image: FAT%d, %v; want FAT%d", v.bits, err, spec.bits)
	}
	setFAT := func(c, n uint32) {
		for i := range int64(2) {
			fat := disk[base+v.fatStart+i*v.fatSize:]
			switch spec.bits {
			case 12:
				off := c + c/2
				old := binary.LittleEndian.Uint16(fat[off:])
				if c&1 == 1 {
					binary.LittleEndian.PutUint16(fat[off:], old&0x000F|uint16(n&0xFFF)<<4)
				} else {
					binary.LittleEndian.PutUint16(fat[off:], old&0xF000|uint16(n&0xFFF))
				}
			case 16:
				binary.LittleEndian.PutUint16(fat[c*2:], uint16(n))
			default:
				binary.LittleEndian.PutUint32(fat[c*4:], n)
			}
		}
	}
	setFAT(0, 0x0FFFFFF8)
	setFAT(1, 0x0FFFFFFF)
	free := uint32(2)
	rootOff, rootSize := v.rootStart, v.rootSize
	if spec.bits == 32 {
		setFAT(2, 0x0FFFFFFF)
		rootOff, rootSize, free = v.clusterOffset(2), v.clusterSize, 3
	}
	slots := disk[base+rootOff : base+rootOff+rootSize]
	used := 0
	addEntry := func(name string, cluster uint32, size int) {
		e := slots[used*32:]
		copy(e[:11], name)
		e[11] = 0x20
		binary.LittleEndian.PutUint16(e[20:], uint16(cluster>>16))
		binary.LittleEndian.PutUint16(e[26:], uint16(cluster))
		binary.LittleEndian.PutUint32(e[28:], uint32(size))
		used++
	}
	if spec.issue != "" {
		copy(disk[base+v.clusterOffset(free):], spec.issue)
		setFAT(free, 0x0FFFFFFF)
		addEntry("ISSUE   TXT", free, len(spec.issue))
	}
	for _, name := range spec.extraRoot {
		addEntry(name, 0, 0)
	}
	for i := 0; spec.rootFull && used < len(slots)/32; i++ {
		addEntry(fmt.Sprintf("F%03d    BIN", i), 0, 0)
	}
	return disk
}

const piIssue = "Raspberry Pi reference 2025-05-13\nGenerated using pi-gen, https://github.com/RPi-Distro/pi-gen\n"

func TestIsRaspiOSImage(t *testing.T) {
	for _, c := range []struct {
		desc string
		name string
		spec fatSpec
		want bool
	}{
		{"FAT32 with issue.txt", "2025-05-13-image.img", fatSpec{bits: 32, issue: piIssue}, true},
		{"FAT16 with issue.txt", "a.img", fatSpec{bits: 16, issue: piIssue}, true},
		{"FAT12 with issue.txt", "a.img", fatSpec{bits: 12, issue: piIssue}, true},
		{"GPT", "a.img", fatSpec{bits: 32, gpt: true, issue: piIssue}, true},
		{"built with pi-gen", "a.img", fatSpec{bits: 32, issue: "MyOS 2026-01-02\nGenerated using pi-gen, https://github.com/RPi-Distro/pi-gen, 1a2b, stage2\n"}, true},
		{"other issue.txt", "ubuntu-server.img", fatSpec{bits: 32, issue: "Ubuntu 24.04\n"}, false},
		{"no issue.txt", "armbian.img", fatSpec{bits: 32}, false},
		{"old one, by its name", "2017-04-10-raspbian-jessie-lite.img", fatSpec{bits: 16}, true},
		{"raspios by name", "raspios-bookworm-arm64.img", fatSpec{bits: 32}, true},
	} {
		if got := IsRaspiOSImage(c.name, buildFATImage(t, c.spec)); got != c.want {
			t.Errorf("%s: IsRaspiOSImage = %v, want %v", c.desc, got, c.want)
		}
	}
	// Named after it, but no FAT partition: nothing to enable SSH on.
	if IsRaspiOSImage("raspios.img", memDisk(testImage(64<<10))) {
		t.Error("an image without partitions is taken for Raspberry Pi OS")
	}
}

// rootHas reports whether the image's boot partition's root folder holds
// name (8.3, padded).
func rootHas(t *testing.T, disk memDisk, name string) bool {
	t.Helper()
	v, err := findBootFAT(disk)
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := v.findRoot(name)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestEnableRaspiSSH(t *testing.T) {
	when := time.Date(2026, 10, 24, 15, 30, 42, 0, time.UTC)
	for _, bits := range []int{12, 16, 32} {
		disk := buildFATImage(t, fatSpec{bits: bits, issue: piIssue, extraRoot: []string{"CONFIG  TXT"}})
		if err := SetupRaspiOS(disk, RaspiSetup{SSH: true}, when); err != nil {
			t.Fatalf("FAT%d: %v", bits, err)
		}
		v, _ := findBootFAT(disk)
		e, ok, _ := v.findRoot("SSH        ")
		if !ok {
			t.Fatalf("FAT%d: no ssh file", bits)
		}
		if size := binary.LittleEndian.Uint32(e[28:]); size != 0 || e[11] != 0x20 || e[12] != 0x08 {
			t.Errorf("FAT%d: entry % x, want an empty file shown in lower case", bits, e)
		}
		if date, tm := fatTime(when); binary.LittleEndian.Uint16(e[24:]) != date || binary.LittleEndian.Uint16(e[22:]) != tm {
			t.Errorf("FAT%d: wrong time stamp", bits)
		}
		if !rootHas(t, disk, "ISSUE   TXT") || !rootHas(t, disk, "CONFIG  TXT") {
			t.Errorf("FAT%d: other files lost", bits)
		}
		if !IsRaspiOSImage("x.img", disk) {
			t.Errorf("FAT%d: issue.txt no longer read", bits)
		}
	}
}

// A file already there, ssh or ssh.txt, is left as it is.
func TestEnableRaspiSSHAlreadyOn(t *testing.T) {
	for _, name := range []string{"SSH        ", "SSH     TXT"} {
		disk := buildFATImage(t, fatSpec{bits: 32, issue: piIssue, extraRoot: []string{name}})
		before := bytes.Clone(disk)
		if err := SetupRaspiOS(disk, RaspiSetup{SSH: true}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(disk, before) {
			t.Errorf("%q there: image changed", name)
		}
	}
}

// A full FAT32 root folder grows by a cluster, in both FATs.
func TestEnableRaspiSSHGrowsRoot(t *testing.T) {
	disk := buildFATImage(t, fatSpec{bits: 32, issue: piIssue, rootFull: true})
	if err := SetupRaspiOS(disk, RaspiSetup{SSH: true}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !rootHas(t, disk, "SSH        ") || !rootHas(t, disk, "F000    BIN") {
		t.Fatal("no ssh file after growing the root folder, or files lost")
	}
	v, _ := findBootFAT(disk)
	offs, _, _ := v.rootChunks()
	if len(offs) != 2 {
		t.Fatalf("root folder of %d clusters, want 2", len(offs))
	}
	fat1 := disk[v.base+v.fatStart : v.base+v.fatStart+v.fatSize]
	fat2 := disk[v.base+v.fatStart+v.fatSize : v.base+v.fatStart+2*v.fatSize]
	if !bytes.Equal(fat1, fat2) {
		t.Error("the two FATs differ")
	}
	fsi := disk[v.base+v.fsInfo:]
	if free := binary.LittleEndian.Uint32(fsi[488:]); free != 66000-3 {
		t.Errorf("FSInfo free count %d, want %d", free, 66000-3)
	}
}

// A full FAT12/16 root folder can't grow: refused, untouched.
func TestEnableRaspiSSHFixedRootFull(t *testing.T) {
	disk := buildFATImage(t, fatSpec{bits: 16, issue: piIssue, rootFull: true})
	before := bytes.Clone(disk)
	if err := SetupRaspiOS(disk, RaspiSetup{SSH: true}, time.Now()); err == nil {
		t.Fatal("a full FAT16 root folder took a file")
	}
	if !bytes.Equal(disk, before) {
		t.Error("image changed")
	}
}

func TestEnableRaspiSSHNoFAT(t *testing.T) {
	if err := SetupRaspiOS(memDisk(testImage(64<<10)), RaspiSetup{SSH: true}, time.Now()); err == nil {
		t.Fatal("SSH enabled on an image without a FAT partition")
	}
}

// The files are added to filesystems mkfs.fat makes, an old userconf.txt
// of several clusters replaced, as fsck.fat finds them sound: checked
// where both tools are installed.
func TestSetupRaspiOSOnMkfsFAT(t *testing.T) {
	mkfs, err1 := exec.LookPath("mkfs.fat")
	fsck, err2 := exec.LookPath("fsck.fat")
	if err1 != nil || err2 != nil {
		t.Skip("mkfs.fat or fsck.fat not installed")
	}
	for _, c := range []struct {
		bits, perCluster string
		files            int // in the root folder before: 70 fill several clusters
	}{{"16", "4", 3}, {"32", "1", 3}, {"32", "1", 70}} {
		part := filepath.Join(t.TempDir(), "part")
		os.WriteFile(part, nil, 0o600)
		os.Truncate(part, 64<<20)
		if out, err := exec.Command(mkfs, "-F", c.bits, "-s", c.perCluster, part).CombinedOutput(); err != nil {
			t.Fatalf("mkfs.fat: %v\n%s", err, out)
		}
		fsData, _ := os.ReadFile(part)
		disk := make(memDisk, testPartLBA*512+len(fsData))
		disk[446+4] = 0x0C
		binary.LittleEndian.PutUint32(disk[446+8:], testPartLBA)
		binary.LittleEndian.PutUint32(disk[446+12:], uint32(len(fsData)/512))
		disk[510], disk[511] = 0x55, 0xAA
		copy(disk[testPartLBA*512:], fsData)
		v, err := findBootFAT(disk)
		if err != nil {
			t.Fatal(err)
		}
		v.w = disk
		for i := range c.files {
			if err := v.writeRootFile(fmt.Sprintf("F%03d    BIN", i), nil, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if err := v.writeRootFile("USERCONFTXT", bytes.Repeat([]byte("x"), 5000), time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := SetupRaspiOS(disk, RaspiSetup{SSH: true, User: &testUser}, time.Now()); err != nil {
			t.Fatalf("FAT%s, %d files: %v", c.bits, c.files, err)
		}
		os.WriteFile(part, disk[testPartLBA*512:], 0o600)
		out, err := exec.Command(fsck, "-n", "-v", part).CombinedOutput()
		if err != nil {
			t.Fatalf("FAT%s, %d files: fsck.fat: %v\n%s", c.bits, c.files, err, out)
		}
		// Its own files, then ssh and userconf.txt.
		if want := fmt.Sprintf(": %d files,", c.files+2); !bytes.Contains(out, []byte(want)) {
			t.Errorf("FAT%s, %d files: fsck.fat doesn't count %q:\n%s", c.bits, c.files, want, out)
		}
		if !rootHas(t, disk, "SSH        ") {
			t.Errorf("FAT%s, %d files: no ssh file", c.bits, c.files)
		}
		if got := userconf(t, disk, "USERCONFTXT"); got != testUserLine {
			t.Errorf("FAT%s, %d files: userconf.txt %q, want %q", c.bits, c.files, got, testUserLine)
		}
	}
}

var testUser = RaspiUser{Name: "massimo", PasswordHash: "$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"}

var testUserLine = testUser.Name + ":" + testUser.PasswordHash + "\n"

// userconf returns what the root folder's file name (8.3, padded) holds.
func userconf(t *testing.T, disk memDisk, name string) string {
	t.Helper()
	v, err := findBootFAT(disk)
	if err != nil {
		t.Fatal(err)
	}
	e, ok, err := v.findRoot(name)
	if err != nil || !ok {
		t.Fatalf("no %q: %v", name, err)
	}
	data, err := v.readFile(e, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// freeClusters counts the volume's free clusters, from its first FAT.
func freeClusters(t *testing.T, disk memDisk) int {
	t.Helper()
	v, _ := findBootFAT(disk)
	free := 0
	for c := uint32(2); c < v.clusters+2; c++ {
		if n, _, _ := v.next(c); n == 0 {
			free++
		}
	}
	return free
}

func TestSetupRaspiOSUser(t *testing.T) {
	for _, bits := range []int{12, 16, 32} {
		disk := buildFATImage(t, fatSpec{bits: bits, issue: piIssue})
		free := freeClusters(t, disk)
		if err := SetupRaspiOS(disk, RaspiSetup{User: &testUser}, time.Now()); err != nil {
			t.Fatalf("FAT%d: %v", bits, err)
		}
		if got := userconf(t, disk, "USERCONFTXT"); got != testUserLine {
			t.Errorf("FAT%d: userconf.txt %q, want %q", bits, got, testUserLine)
		}
		v, _ := findBootFAT(disk)
		if e, _, _ := v.findRoot("USERCONFTXT"); e[12] != 0x18 {
			t.Errorf("FAT%d: case flags %#x, want userconf.txt shown in lower case", bits, e[12])
		}
		if rootHas(t, disk, "SSH        ") {
			t.Errorf("FAT%d: SSH enabled though not asked", bits)
		}
		if got := freeClusters(t, disk); got != free-1 {
			t.Errorf("FAT%d: %d free clusters, want %d", bits, got, free-1)
		}
		if got := userconf(t, disk, "ISSUE   TXT"); got != piIssue {
			t.Errorf("FAT%d: issue.txt now %q", bits, got)
		}
	}
}

// One there already is replaced, its clusters freed; userconf, read first
// by Raspberry Pi OS, is the one replaced should both be there.
func TestSetupRaspiOSUserReplaced(t *testing.T) {
	for _, name := range []string{"USERCONFTXT", "USERCONF   "} {
		disk := buildFATImage(t, fatSpec{bits: 16, issue: piIssue, extraRoot: []string{"USERCONFTXT"}})
		v, _ := findBootFAT(disk)
		v.w = disk
		if err := v.writeRootFile(name, bytes.Repeat([]byte("old "), 1000), time.Now()); err != nil {
			t.Fatal(err)
		}
		free := freeClusters(t, disk)
		if err := SetupRaspiOS(disk, RaspiSetup{SSH: true, User: &testUser}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := userconf(t, disk, name); got != testUserLine {
			t.Errorf("%q: holds %q, want %q", name, got, testUserLine)
		}
		if got, want := freeClusters(t, disk), free+8-1; got != want {
			t.Errorf("%q: %d free clusters, want %d (the old 8 freed, 1 taken)", name, got, want)
		}
	}
}

// A file of several clusters is written whole, its chain in both FATs.
func TestWriteRootFileSeveralClusters(t *testing.T) {
	for _, bits := range []int{12, 16, 32} {
		disk := buildFATImage(t, fatSpec{bits: bits})
		v, _ := findBootFAT(disk)
		v.w = disk
		data := testImage(5*512 + 17)
		if err := v.writeRootFile("BIG     BIN", data, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := userconf(t, disk, "BIG     BIN"); got != string(data) {
			t.Errorf("FAT%d: file read back differs", bits)
		}
		fat1 := disk[v.base+v.fatStart : v.base+v.fatStart+v.fatSize]
		fat2 := disk[v.base+v.fatStart+v.fatSize : v.base+v.fatStart+2*v.fatSize]
		if !bytes.Equal(fat1, fat2) {
			t.Errorf("FAT%d: the two FATs differ", bits)
		}
	}
}

func TestNewRaspiUser(t *testing.T) {
	for _, c := range []struct {
		name, password string
		ok             bool
	}{
		{"pi", "raspberry", true},
		{"a-1", "x", true},
		{"", "x", false},
		{"root", "x", false},
		{"Massimo", "x", false},
		{"1pi", "x", false},
		{"-pi", "x", false},
		{"pi_user", "x", false},
		{"pi:x", "x", false},
		{"abcdefghijklmnopqrstuvwxyzabcdefg", "x", false},
		{"pi", "", false},
	} {
		u, err := NewRaspiUser(c.name, c.password)
		if (err == nil) != c.ok {
			t.Errorf("NewRaspiUser(%q, %q): %v, want ok %v", c.name, c.password, err, c.ok)
			continue
		}
		if c.ok && (u.Name != c.name || u.PasswordHash != sha512CryptSalt(c.password, strings.Split(u.PasswordHash, "$")[2])) {
			t.Errorf("NewRaspiUser(%q): %+v", c.name, u)
		}
	}
}
