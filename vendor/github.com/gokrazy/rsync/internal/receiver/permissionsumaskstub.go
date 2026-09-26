//go:build !(linux || darwin)

package receiver

import "io/fs"

func defaultPerms() fs.FileMode {
	return 0o777
}
