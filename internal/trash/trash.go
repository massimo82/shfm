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

// Package trash implements (for the part relevant to an interactive file
// manager) the Freedesktop.org Trash Specification 1.0:
// https://specifications.freedesktop.org/trash-spec/trashspec-1.0.html
//
// It handles both the "home" trash ($XDG_DATA_HOME/Trash) and, when a file
// being trashed lives on a filesystem/mount point other than $HOME's, the
// "top directory" trash cans ($topdir/.Trash/$uid or $topdir/.Trash-$uid) as
// specified, to avoid expensive copies across different filesystems.
package trash

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Item represents an entry present in the trash.
type Item struct {
	// TrashRoot is the root folder of the trash can containing this item
	// (files/ e info/).
	TrashRoot string
	// ID is the file name inside files/ and info/ (without the .trashinfo extension).
	ID string
	// OriginalPath is the original absolute path of the file before deletion.
	OriginalPath string
	// DeletionDate is the deletion date/time.
	DeletionDate time.Time
	// IsDir indicates whether the trashed item was a folder.
	IsDir bool
}

// FilesPath returns the path of the trashed content.
func (it Item) FilesPath() string { return filepath.Join(it.TrashRoot, "files", it.ID) }

// InfoPath returns the path of the associated .trashinfo file.
func (it Item) InfoPath() string { return filepath.Join(it.TrashRoot, "info", it.ID+".trashinfo") }

// homeTrashDir returns $XDG_DATA_HOME/Trash, creating it (with the
// files/ and info/ subfolders) if it doesn't exist, with 0700
// permissions as required by the spec.
func homeTrashDir() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	root := filepath.Join(dataHome, "Trash")
	if err := ensureTrashDirs(root); err != nil {
		return "", err
	}
	return root, nil
}

func ensureTrashDirs(root string) error {
	for _, sub := range []string{"files", "info"} {
		p := filepath.Join(root, sub)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// sameDevice reports whether two paths reside on the same filesystem/device.
func sameDevice(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return false
	}
	return sameDeviceInfo(sa, sb)
}

// topDirTrash looks for (or creates, if we have permission) a "top
// directory" trash can for the filesystem path is on, per section
// "Trash directories" §2 of the spec: first $topdir/.Trash/$uid (if
// $topdir/.Trash exists, is not a symlink, and has the sticky bit set),
// altrimenti $topdir/.Trash-$uid.
func topDirTrash(path string) (string, bool) {
	top := findMountPoint(path)
	if top == "" {
		return "", false
	}
	uid := os.Getuid()

	dotTrash := filepath.Join(top, ".Trash")
	if fi, err := os.Lstat(dotTrash); err == nil && fi.Mode()&os.ModeSymlink == 0 && fi.IsDir() {
		if fi.Mode().Perm()&os.ModeSticky != 0 || fi.Mode()&os.ModeSticky != 0 {
			candidate := filepath.Join(dotTrash, strconv.Itoa(uid))
			if ensureTrashDirs(candidate) == nil {
				return candidate, true
			}
		}
	}
	candidate := filepath.Join(top, fmt.Sprintf(".Trash-%d", uid))
	if err := ensureTrashDirs(candidate); err == nil {
		return candidate, true
	}
	return "", false
}

// MoveToTrash trashes path (a file or folder) following the Trash Specification:
// picks the "top directory" trash can if path is on a filesystem other than
// $HOME (to avoid a copy across devices), otherwise the home trash can.
// Writes the .trashinfo file with Path and DeletionDate, handling name
// collisions with a numeric suffix.
func MoveToTrash(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	root, err := homeTrashDir()
	if err != nil {
		return err
	}
	if home != "" && !sameDevice(absPath, home) {
		if td, ok := topDirTrash(absPath); ok {
			root = td
		}
	}

	base := filepath.Base(absPath)
	id := base
	filesDest := filepath.Join(root, "files", id)
	suffix := 1
	for {
		if _, err := os.Lstat(filesDest); os.IsNotExist(err) {
			break
		}
		id = fmt.Sprintf("%s.%d", base, suffix)
		filesDest = filepath.Join(root, "files", id)
		suffix++
	}

	// Write the .trashinfo file first (as recommended by the spec, to avoid
	// a window where the file exists in files/ without metadata), then move
	// the content.
	infoPath := filepath.Join(root, "info", id+".trashinfo")
	if err := writeTrashInfo(infoPath, absPath); err != nil {
		return err
	}
	if err := os.Rename(absPath, filesDest); err != nil {
		// rename cross-device: fall back to copy+delete
		if err := copyAny(absPath, filesDest); err != nil {
			os.Remove(infoPath)
			return err
		}
		if err := os.RemoveAll(absPath); err != nil {
			// The content is already copied into the trash, but the
			// original couldn't be removed (e.g. a directory permission
			// problem the caller might retry with elevated privileges) —
			// leaving both the copy and its metadata behind would orphan
			// a trash entry for a file that, as far as the caller's
			// concerned, was never actually trashed. Clean up so
			// MoveToTrash leaves no trace at all when it returns an error,
			// same as the copyAny failure case just above.
			os.RemoveAll(filesDest)
			os.Remove(infoPath)
			return err
		}
	}
	_ = info
	return nil
}

func writeTrashInfo(infoPath, originalPath string) error {
	content := "[Trash Info]\n" +
		"Path=" + encodeTrashPath(originalPath) + "\n" +
		"DeletionDate=" + time.Now().Format("2006-01-02T15:04:05") + "\n"
	f, err := os.OpenFile(infoPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

// encodeTrashPath applies the percent-encoding the spec requires for
// "unreserved" characters in the Path field of the .trashinfo file (RFC
// 3986, like a URL but without encoding '/').
func encodeTrashPath(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == '~', r == '/':
			b.WriteRune(r)
		default:
			for _, c := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", c)
			}
		}
	}
	return b.String()
}

func decodeTrashPath(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			var v int
			if _, err := fmt.Sscanf(s[i+1:i+3], "%02X", &v); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// List lists every item present in the user's home trash can.
// (Top-directory trash cans of any mounted devices can be added by
// passing their respective roots to ListRoot.)
func List() ([]Item, error) {
	root, err := homeTrashDir()
	if err != nil {
		return nil, err
	}
	return ListRoot(root)
}

// ListRoot lists the items of a specific trash can (root must contain
// the files/ and info/ subfolders).
func ListRoot(root string) ([]Item, error) {
	infoDir := filepath.Join(root, "info")
	des, err := os.ReadDir(infoDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var items []Item
	for _, de := range des {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".trashinfo") {
			continue
		}
		id := strings.TrimSuffix(de.Name(), ".trashinfo")
		data, err := os.ReadFile(filepath.Join(infoDir, de.Name()))
		if err != nil {
			continue
		}
		item := Item{TrashRoot: root, ID: id}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Path=") {
				item.OriginalPath = decodeTrashPath(strings.TrimPrefix(line, "Path="))
			} else if strings.HasPrefix(line, "DeletionDate=") {
				t, _ := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimPrefix(line, "DeletionDate="), time.Local)
				item.DeletionDate = t
			}
		}
		if fi, err := os.Lstat(item.FilesPath()); err == nil {
			item.IsDir = fi.IsDir()
		}
		items = append(items, item)
	}
	return items, nil
}

// Restore restores a trashed item to its original location.
// If an item with the same name already exists at the original location,
// it returns an error instead of overwriting it.
func Restore(it Item) error {
	if it.OriginalPath == "" {
		return fmt.Errorf("unknown original path for %q", it.ID)
	}
	if _, err := os.Lstat(it.OriginalPath); err == nil {
		return fmt.Errorf("an item already exists at %q", it.OriginalPath)
	}
	if err := os.MkdirAll(filepath.Dir(it.OriginalPath), 0o755); err != nil {
		return err
	}
	if err := os.Rename(it.FilesPath(), it.OriginalPath); err != nil {
		if err := copyAny(it.FilesPath(), it.OriginalPath); err != nil {
			return err
		}
		if err := os.RemoveAll(it.FilesPath()); err != nil {
			return err
		}
	}
	return os.Remove(it.InfoPath())
}

// Purge permanently deletes an item from the trash.
func Purge(it Item) error {
	if err := os.RemoveAll(it.FilesPath()); err != nil {
		return err
	}
	return os.Remove(it.InfoPath())
}

// Empty completely empties the user's home trash can.
func Empty() error {
	items, err := List()
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := Purge(it); err != nil {
			return err
		}
	}
	return nil
}
