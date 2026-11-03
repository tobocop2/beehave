// Package atomicfile writes a file so that a crash leaves the old file or the new file, never a part of one.
package atomicfile

import (
	"os"
	"path/filepath"

	"github.com/google/renameio/v2"
)

// Write replaces the file at path with data and gives it the mode perm, also when the file exists. It writes a temporary file in the same
// folder, syncs it, then renames it over the target. renameio.WriteFile is not used because it adds WithExistingPermissions, which keeps the
// mode of an existing target (renameio v2.0.2, writefile.go:23-26); WithStaticPermissions sets the mode every time (option.go:56-60).
func Write(path string, data []byte, perm os.FileMode) error {
	file, err := renameio.NewPendingFile(path, renameio.WithTempDir(filepath.Dir(path)), renameio.WithStaticPermissions(perm))
	if err != nil {
		return err
	}
	defer file.Cleanup() //nolint:errcheck // Cleanup after a rename has nothing to remove; a failure here cannot change the result

	_, err = file.Write(data)
	if err == nil {
		err = file.CloseAtomicallyReplace()
	}

	return err
}
