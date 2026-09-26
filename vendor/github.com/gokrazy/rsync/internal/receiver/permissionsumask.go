//go:build linux || darwin

package receiver

import (
	"io/fs"
	"sync"
	"syscall"
)

var umask = sync.OnceValue(func() fs.FileMode {
	// To learn the umask, we must clear it and restore it.
	umask := syscall.Umask(0)
	syscall.Umask(umask)
	return fs.FileMode(umask)
})

func defaultPerms() fs.FileMode {
	return 0o777 &^ umask()
}
