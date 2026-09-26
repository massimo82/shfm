//go:build !linux && !darwin

package receiver

import "io/fs"

func (rt *Transfer) createDevice(*File, fs.FileInfo, fs.FileMode) error {
	return nil
}
