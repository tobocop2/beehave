package flow

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/gofrs/flock"

	"github.com/tobocop2/obee/internal/lockfile"
)

const engineLockName = "engine.lock"

// errStateBusy is returned when another engine holds the state folder.
var errStateBusy = errors.New("another obee run owns this state folder; wait for it, or see obee status")

// lockFile names a lock file in dir.
func lockFile(dir, name string) *flock.Flock {
	return lockfile.Open(filepath.Join(dir, name))
}

// engineRuns reports whether an engine holds the state folder. It takes the engine lock for the moment of the check, as giveBackUnowned does.
func engineRuns(dir string) (bool, error) {
	held, err := holdEngine(dir)
	if errors.Is(err, errStateBusy) {
		return true, nil
	}

	if err != nil {
		return false, err
	}

	return false, held.Unlock()
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
