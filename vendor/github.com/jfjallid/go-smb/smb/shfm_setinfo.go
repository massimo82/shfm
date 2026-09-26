// Local addition for shfm (not part of upstream go-smb): SET_INFO requests
// to truncate/extend an open file, rename a file or directory and set its
// timestamps, and a QUERY_INFO request for the share's size and free space,
// which the upstream public API doesn't expose. Needed by shfm's FUSE mount
// of an SMB share, where applications truncate files, save by renaming a
// temporary file over the original, preserve modification times (cp -p,
// rsync -t) and check free space.

package smb

import (
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/jfjallid/go-smb/smb/unicode"
)

// setInfo sends an SMB2 SET_INFO request of class infoClass on the open
// file f, with buffer as the class-specific payload.
func (f *File) setInfo(op string, infoClass byte, buffer []byte) error {
	if f.fd == nil {
		return fmt.Errorf("can't operate on a closed file")
	}
	req, err := f.NewSetInfoReq(f.share, f.fd)
	if err != nil {
		return err
	}
	req.InfoType = OInfoFile
	req.FileInfoClass = infoClass
	req.Buffer = buffer
	buf, err := f.sendrecv(&req)
	if err != nil {
		return err
	}
	_, err = headerStatus(op, buf)
	return err
}

// SetEndOfFile truncates or extends the open file to size bytes
// (FileEndOfFileInformation, MS-FSCC 2.4.13). The file must have been
// opened with write access.
func (f *File) SetEndOfFile(size uint64) error {
	buffer := binary.LittleEndian.AppendUint64(nil, size)
	return f.setInfo("SetInfo (end of file)", FileEndOfFileInformation, buffer)
}

// Rename renames/moves the open file or directory to newPath, a path
// relative to the root of the same share (MS-FSCC 2.4.37,
// FILE_RENAME_INFORMATION_TYPE_2). If replace is true an existing
// destination file is overwritten. The file must have been opened with
// DELETE access.
func (f *File) Rename(newPath string, replace bool) error {
	newPath = strings.ReplaceAll(newPath, `/`, `\`)
	newPath = strings.Trim(newPath, `\`)
	name := unicode.ToUnicode(newPath)

	buffer := make([]byte, 0, 20+len(name))
	if replace {
		buffer = append(buffer, 1)
	} else {
		buffer = append(buffer, 0)
	}
	buffer = append(buffer, make([]byte, 7)...)                          // Reserved
	buffer = binary.LittleEndian.AppendUint64(buffer, 0)                 // RootDirectory: must be 0 over SMB2
	buffer = binary.LittleEndian.AppendUint32(buffer, uint32(len(name))) // FileNameLength
	buffer = append(buffer, name...)
	return f.setInfo("SetInfo (rename)", FileRenameInformation, buffer)
}

// fileTime converts t to a Windows FILETIME; the zero time gives 0, which
// in a SET_INFO request means "leave unchanged".
func fileTime(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	const epochDiff = 116444736000000000 // 1601-01-01 -> 1970-01-01 in 100ns units
	return uint64(t.UnixNano()/100) + epochDiff
}

// SetTimes sets the open file's last access and last write times
// (FileBasicInformation, MS-FSCC 2.4.7); a zero time is left unchanged. The
// file must have been opened with FILE_WRITE_ATTRIBUTES access.
func (f *File) SetTimes(atime, mtime time.Time) error {
	buffer := make([]byte, 0, 40)
	buffer = binary.LittleEndian.AppendUint64(buffer, 0) // CreationTime: unchanged
	buffer = binary.LittleEndian.AppendUint64(buffer, fileTime(atime))
	buffer = binary.LittleEndian.AppendUint64(buffer, fileTime(mtime))
	buffer = binary.LittleEndian.AppendUint64(buffer, 0) // ChangeTime: unchanged
	buffer = binary.LittleEndian.AppendUint32(buffer, 0) // FileAttributes: unchanged
	buffer = binary.LittleEndian.AppendUint32(buffer, 0) // Reserved
	return f.setInfo("SetInfo (times)", FileBasicInformation, buffer)
}

// fileFsFullSizeInformation is the FileFsFullSizeInformation class of
// filesystem QUERY_INFO requests (MS-FSCC 2.5.4).
const fileFsFullSizeInformation byte = 7

// FsSize returns the total size and the free space available to the user,
// in bytes, of the volume holding the open file or directory.
func (f *File) FsSize() (total, free uint64, err error) {
	if f.fd == nil {
		return 0, 0, fmt.Errorf("can't operate on a closed file")
	}
	req, err := f.NewQueryInfoReq(f.share, f.fd, OInfoFilesystem, fileFsFullSizeInformation, 0, 0, 32, nil)
	if err != nil {
		return 0, 0, err
	}
	buf, err := f.sendrecv(&req)
	if err != nil {
		return 0, 0, err
	}
	res := &QueryInfoRes{}
	if err := res.UnmarshalBinary(buf); err != nil {
		return 0, 0, err
	}
	if err := statusError("QueryInfo (volume size)", res.Header.Status); err != nil {
		return 0, 0, err
	}
	if len(res.Buffer) < 32 {
		return 0, 0, fmt.Errorf("QueryInfo (volume size): short response (%d bytes)", len(res.Buffer))
	}
	totalUnits := binary.LittleEndian.Uint64(res.Buffer[0:])
	callerFree := binary.LittleEndian.Uint64(res.Buffer[8:])
	unit := uint64(binary.LittleEndian.Uint32(res.Buffer[24:])) * uint64(binary.LittleEndian.Uint32(res.Buffer[28:]))
	return totalUnits * unit, callerFree * unit, nil
}
