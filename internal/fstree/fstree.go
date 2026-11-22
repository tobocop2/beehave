// Package fstree removes a folder tree that holds read-only folders.
package fstree

import (
	"io/fs"
	"os"
	"path/filepath"
)

// ownerOnly is the mode that Remove gives each folder of the tree before its second removal.
const ownerOnly = 0o700

// Remove removes path with all it holds. When that fails, it gives the owner write and search rights on every folder and tries again, because the Go
// command extracts its module cache read-only.
func Remove(path string) error {
	if os.RemoveAll(path) == nil {
		return nil
	}

	//nolint:errcheck // a folder that cannot change mode makes the second removal fail, and that failure is returned
	_ = filepath.WalkDir(path, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}

		return os.Chmod(p, ownerOnly) //nolint:gosec // the folder is inside a tree that the caller removes
	})

	return os.RemoveAll(path)
}
