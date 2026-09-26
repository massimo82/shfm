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

package vfs

import (
	"errors"
	"os"
	"syscall"

	"github.com/jfjallid/go-smb/smb"
	"github.com/pkg/sftp"
	nfsc "github.com/vmware/go-nfs-client/nfs"
)

// IsConnectionFailure reports whether err, returned by a network backend,
// may mean the connection to the server is gone — so reconnecting and
// retrying could succeed — rather than being the server's own answer to
// the request (no such file, access denied, directory not empty, ...),
// which a retry would only repeat.
//
// It errs on the side of "maybe": go-smb reports a closed connection with
// plain, untyped errors, so anything that isn't recognizably an answer
// from the server counts as a possible connection failure.
func IsConnectionFailure(err error) bool {
	if err == nil || errors.Is(err, ErrNotSupported) {
		return false
	}
	if errors.Is(err, sftp.ErrSSHFxConnectionLost) {
		return true
	}

	var smbStatus *smb.NTStatusError
	if errors.As(err, &smbStatus) {
		switch smbStatus.Status {
		case smb.StatusNetworkNameDeleted, smb.StatusUserSessionDeleted,
			0xc000035c: // STATUS_NETWORK_SESSION_EXPIRED
			return true
		}
		return false
	}
	var nfsErr *nfsc.Error
	if errors.As(err, &nfsErr) {
		// A stale handle can be the export's root handle after the server
		// restarted: a new mount gets a fresh one.
		return nfsErr.ErrorNum == nfsc.NFS3ErrStale || nfsErr.ErrorNum == nfsc.NFS3ErrBadHandle
	}
	var sftpStatus *sftp.StatusError
	if errors.As(err, &sftpStatus) {
		return false
	}

	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.ECONNRESET, syscall.ECONNABORTED, syscall.ECONNREFUSED, syscall.EPIPE,
			syscall.ENOTCONN, syscall.ETIMEDOUT, syscall.EHOSTUNREACH, syscall.ENETUNREACH,
			syscall.ENETDOWN:
			return true
		}
		return false
	}
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, os.ErrExist),
		errors.Is(err, os.ErrPermission), errors.Is(err, os.ErrInvalid):
		return false
	}
	return true
}
