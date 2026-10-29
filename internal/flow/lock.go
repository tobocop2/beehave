package flow

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/gofrs/flock"

	"github.com/tobocop2/obee/internal/lockfile"
)

const (
	engineLockName  = "engine.lock"
	migrateLockName = "migrate.lock"
)

// errStateBusy is returned when another engine holds the state folder.
var errStateBusy = errors.New("another obee run owns this state folder; wait for it, or see obee status")

// lockFile names a lock file in dir.
func lockFile(dir, name string) *flock.Flock {
	return lockfile.Open(filepath.Join(dir, name))
}

// holdEngine takes the engine lock, which one engine holds from NewEngine until Shutdown.
func holdEngine(dir string) (*flock.Flock, error) {
	held := lockFile(dir, engineLockName)

	taken, err := held.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", engineLockName, err)
	}

	if !taken {
		return nil, errStateBusy
	}

	return held, nil
}

// migrating runs open under the migrate lock. The first open of a new state folder creates and migrates the database;
// the migration of the workflow library has no lock of its own, so two processes that open a new folder fail.
func migrating(dir string, open func() error) error {
	held := lockFile(dir, migrateLockName)
	if err := held.Lock(); err != nil {
		return fmt.Errorf("lock %s: %w", migrateLockName, err)
	}

	return errors.Join(open(), held.Unlock())
}
