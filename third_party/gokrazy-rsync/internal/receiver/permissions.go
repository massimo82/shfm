package receiver

import (
	"io/fs"
	"os"
)

func (rt *Transfer) destPerm(f *File, st os.FileInfo) fs.FileMode {
	senderPerm := fs.FileMode(f.Mode).Perm()
	if rt.Opts.PreservePerms {
		return senderPerm // --perms means apply the sender permissions
	}
	if st != nil {
		return st.Mode().Perm() // keep existing permissions
	}
	return senderPerm & rt.defaultPerms
}
