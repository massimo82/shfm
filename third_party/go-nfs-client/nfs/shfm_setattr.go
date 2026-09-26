// Local addition for shfm (not part of upstream go-nfs-client): the
// NFSPROC3_SETATTR (truncate, chmod, chown, timestamps), NFSPROC3_RENAME
// and NFSPROC3_FSSTAT procedures, which upstream doesn't implement, and
// errno mapping for NFS errors. Needed by shfm's FUSE mount of an NFS
// export, where applications truncate files, save by renaming a temporary
// file over the original, preserve timestamps and check free space.

package nfs

import (
	"path/filepath"
	"syscall"
	"time"

	"github.com/vmware/go-nfs-client/nfs/rpc"
	"github.com/vmware/go-nfs-client/nfs/xdr"
)

const (
	NFSProc3SetAttr = 2
	NFSProc3Rename  = 14
	NFSProc3FSStat  = 18
)

// Unwrap exposes the NFS error as the matching errno: NFSv3 status codes
// below 10000 are defined to share their values with the Unix errno ones
// (RFC 1813, section 2.6), so callers can use errors.Is/As with syscall
// errnos and os.ErrNotExist & co.
func (err *Error) Unwrap() error {
	if err.ErrorNum < 10000 {
		return syscall.Errno(err.ErrorNum)
	}
	if err.ErrorNum == NFS3ErrNotSupp {
		return syscall.ENOTSUP
	}
	return nil
}

// setAttr applies attr to the object identified by fh (RFC 1813, 3.3.2).
func (v *Target) setAttr(fh []byte, attr Sattr3) error {
	type SattrGuard3 struct {
		Check bool     `xdr:"union"`
		Ctime NFS3Time `xdr:"unioncase=1"`
	}
	type SetAttr3Args struct {
		rpc.Header
		Object []byte
		Attr   Sattr3
		Guard  SattrGuard3
	}

	_, err := v.call(&SetAttr3Args{
		Header: rpc.Header{
			Rpcvers: 2,
			Prog:    Nfs3Prog,
			Vers:    Nfs3Vers,
			Proc:    NFSProc3SetAttr,
			Cred:    v.auth,
			Verf:    rpc.AuthNull,
		},
		Object: fh,
		Attr:   attr,
	})
	return err
}

func (v *Target) setSize(fh []byte, size uint64) error {
	return v.setAttr(fh, Sattr3{Size: SetSize{SetIt: true, Size: size}})
}

// SetAttr applies attr to the object at path (fields left unset are left
// unchanged).
func (v *Target) SetAttr(path string, attr Sattr3) error {
	_, fh, err := v.Lookup(path)
	if err != nil {
		return err
	}
	return v.setAttr(fh, attr)
}

// ClientTime returns a SetTime setting a timestamp to t.
func ClientTime(t time.Time) SetTime {
	return SetTime{SetIt: SetToClientTime, Time: NFS3Time{Seconds: uint32(t.Unix()), Nseconds: uint32(t.Nanosecond())}}
}

// FSStat returns the total size and the free space available to the user,
// in bytes, of the exported filesystem (RFC 1813, 3.3.18).
func (v *Target) FSStat() (total, free uint64, err error) {
	type FSStat3Args struct {
		rpc.Header
		FsRoot []byte
	}
	type FSStat3Res struct {
		Attr     PostOpAttr
		Tbytes   uint64
		Fbytes   uint64
		Abytes   uint64
		Tfiles   uint64
		Ffiles   uint64
		Afiles   uint64
		Invarsec uint32
	}

	res, err := v.call(&FSStat3Args{
		Header: rpc.Header{
			Rpcvers: 2,
			Prog:    Nfs3Prog,
			Vers:    Nfs3Vers,
			Proc:    NFSProc3FSStat,
			Cred:    v.auth,
			Verf:    rpc.AuthNull,
		},
		FsRoot: v.fh,
	})
	if err != nil {
		return 0, 0, err
	}
	stat := new(FSStat3Res)
	if err := xdr.Read(res, stat); err != nil {
		return 0, 0, err
	}
	return stat.Tbytes, stat.Abytes, nil
}

// SetSize truncates or extends the file at path to size bytes.
func (v *Target) SetSize(path string, size uint64) error {
	_, fh, err := v.Lookup(path)
	if err != nil {
		return err
	}
	return v.setSize(fh, size)
}

// Truncate truncates or extends the open file to size bytes.
func (f *File) Truncate(size uint64) error {
	return f.setSize(f.fh, size)
}

// Rename renames/moves from to to (both paths relative to the export
// root), replacing an existing destination as rename(2) does (RFC 1813,
// 3.3.14).
func (v *Target) Rename(from, to string) error {
	type Rename3Args struct {
		rpc.Header
		From Diropargs3
		To   Diropargs3
	}

	fromDir, fromName := filepath.Split(from)
	_, fromFH, err := v.Lookup(fromDir)
	if err != nil {
		return err
	}
	toDir, toName := filepath.Split(to)
	_, toFH, err := v.Lookup(toDir)
	if err != nil {
		return err
	}

	_, err = v.call(&Rename3Args{
		Header: rpc.Header{
			Rpcvers: 2,
			Prog:    Nfs3Prog,
			Vers:    Nfs3Vers,
			Proc:    NFSProc3Rename,
			Cred:    v.auth,
			Verf:    rpc.AuthNull,
		},
		From: Diropargs3{FH: fromFH, Filename: fromName},
		To:   Diropargs3{FH: toFH, Filename: toName},
	})
	return err
}
