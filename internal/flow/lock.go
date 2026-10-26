package flow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

const (
	engineLockName  = "engine.lock"
	migrateLockName = "migrate.lock"
	lockFileMode    = 0o600
)

// errStateBusy is returned when another engine holds the state folder.
var errStateBusy = errors.New("another obee run owns this state folder; wait for it, or see obee status")

// fileLock is an advisory lock on a file of the state folder. The system drops it when the process ends.
type fileLock struct {
	file *os.File
}

// lockFile locks the named file of the state folder. With wait false it fails at once when another holder has the lock.
func lockFile(dir, name string, wait bool) (*fileLock, error) {
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, lockFileMode) //nolint:gosec // the name is one of the fixed lock file names of the state folder
	if err != nil {
		return nil, fmt.Errorf("open the lock file %s: %w", name, err)
	}

	if err := flock(file, wait); err != nil {
		return nil, errors.Join(fmt.Errorf("lock %s: %w", name, err), file.Close())
	}

	return &fileLock{file: file}, nil
}

// flock takes an exclusive lock on the file and retries a call that a signal cut short.
func flock(file *os.File, wait bool) error {
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}

	for {
		if err := syscall.Flock(int(file.Fd()), how); !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// release gives the lock back. Closing the file drops the lock.
func (l *fileLock) release() error {
	return l.file.Close()
}

// holdEngine takes the engine lock, which one engine holds from NewEngine until Shutdown.
func holdEngine(dir string) (*fileLock, error) {
	held, err := lockFile(dir, engineLockName, false)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, errStateBusy
	}

	return held, err
}

// migrating runs open under the migrate lock. The first open of a new state folder creates and migrates the database;
// the migration of the workflow library has no lock of its own, so two processes that open a new folder fail.
func migrating(dir string, open func() error) error {
	held, err := lockFile(dir, migrateLockName, true)
	if err != nil {
		return err
	}

	return errors.Join(open(), held.release())
}
