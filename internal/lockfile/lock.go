// Package lockfile opens the file locks of obee. It is layer 0: it imports no obee package.
package lockfile

import (
	"os"

	"github.com/gofrs/flock"
)

// Open returns a lock on the file at path. It opens the file for reading and writing and creates it,
// so a folder of that name, or a file that the process cannot write, is an error and not a lock.
func Open(path string) *flock.Flock {
	return flock.New(path, flock.SetFlag(os.O_CREATE|os.O_RDWR))
}
