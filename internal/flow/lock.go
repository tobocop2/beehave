package flow

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"

	"github.com/tobocop2/obee/internal/filelock"
)

const (
	engineLockName  = "engine.lock"
	migrateLockName = "migrate.lock"
)

// errStateBusy is returned when another engine holds the state folder.
var errStateBusy = errors.New("another obee run owns this state folder; wait for it, or see obee status")

// holdEngine takes the engine lock, which one engine holds from NewEngine until Shutdown.
func holdEngine(dir string) (*filelock.Lock, error) {
	held, err := filelock.TryExclusive(filepath.Join(dir, engineLockName))
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, errStateBusy
	}

	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", engineLockName, err)
	}

	return held, nil
}

// migrating runs open under the migrate lock. The first open of a new state folder creates and migrates the database;
// the migration of the workflow library has no lock of its own, so two processes that open a new folder fail.
func migrating(dir string, open func() error) error {
	held, err := filelock.Exclusive(filepath.Join(dir, migrateLockName))
	if err != nil {
		return fmt.Errorf("lock %s: %w", migrateLockName, err)
	}

	return errors.Join(open(), held.Release())
}
