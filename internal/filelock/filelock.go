// Package filelock takes advisory file locks with flock. The system drops a lock when the process ends.
package filelock

import (
	"errors"
	"os"
	"syscall"
)

const fileMode = 0o600

// Lock is a lock on a file. Release gives it back.
type Lock struct {
	file *os.File
}

// Exclusive creates the file if it is missing and waits for the exclusive lock on it.
func Exclusive(path string) (*Lock, error) {
	return take(path, syscall.LOCK_EX)
}

// TryExclusive takes the exclusive lock at once, or fails when another holder or sharer has the file. The error then satisfies errors.Is(err, syscall.EWOULDBLOCK).
func TryExclusive(path string) (*Lock, error) {
	return take(path, syscall.LOCK_EX|syscall.LOCK_NB)
}

// Shared waits for a shared lock on the file: other sharers hold it with the caller, and a holder of the exclusive lock does not.
func Shared(path string) (*Lock, error) {
	return take(path, syscall.LOCK_SH)
}

// Release gives the lock back. It unlocks first: a lock belongs to the open file, and a child process that forked
// and did not yet run exec holds a copy of the file, so closing alone would keep the lock until that child execs.
func (l *Lock) Release() error {
	return errors.Join(flock(l.file, syscall.LOCK_UN), l.file.Close())
}

func take(path string, how int) (*Lock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, fileMode) //nolint:gosec // the caller names the lock file
	if err != nil {
		return nil, err //nolint:wrapcheck // the path error already names the file
	}

	if err := flock(file, how); err != nil {
		return nil, errors.Join(err, file.Close())
	}

	return &Lock{file: file}, nil
}

// flock takes a lock of the kind how and retries a call that a signal cut short.
func flock(file *os.File, how int) error {
	for {
		if err := syscall.Flock(int(file.Fd()), how); !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
