package flow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"

	"github.com/tobocop2/beehave/internal/atomicfile"
	"github.com/tobocop2/beehave/internal/lockfile"
)

const (
	engineLockName = "engine.lock"
	// probeLockName names the lock that serializes every check of the engine lock and the start of an engine. A process holds it for microseconds, except giveBackUnowned, which holds it for a release.
	probeLockName = "probe.lock"
	// engineVersionName is the file where the live engine writes its version. It is not the lock file: atomicfile.Write renames a new file over the old one, and a rename over a file that holds a lock would give a second process a new file to lock.
	engineVersionName = "engine.version"
	versionMode       = 0o600
)

// ErrStateBusy is returned when another engine holds the state folder.
var ErrStateBusy = errors.New("another beehave run owns this state folder; wait for it, or see beehave status")

// lockFile names a lock file in dir.
func lockFile(dir, name string) *flock.Flock {
	return lockfile.Open(filepath.Join(dir, name))
}

// engineState reports whether an engine holds the state folder, and the version that engine wrote.
// It holds probe.lock for the whole check, and an engine holds probe.lock while it takes the engine lock and writes its version. So a held engine lock is an engine that has written its version, or a release of giveBackUnowned, which holds probe.lock too and so never meets a check.
// A check that finds the engine lock free removes a version that a stopped engine left.
// The version is empty while an engine shuts down: it removes the file before it frees the lock.
func engineState(dir string) (running bool, version string, err error) {
	probe, err := holdProbe(dir)
	if err != nil {
		return false, "", err
	}

	defer func() { err = errors.Join(err, probe.Unlock()) }()

	engine, err := holdEngine(dir)
	if errors.Is(err, ErrStateBusy) {
		version, err = engineVersion(dir)

		return true, version, err
	}

	if err != nil {
		return false, "", err
	}

	return false, "", errors.Join(removeEngineVersion(dir), engine.Unlock())
}

// engineRuns reports whether an engine holds the state folder.
func engineRuns(dir string) (bool, error) {
	running, _, err := engineState(dir)

	return running, err
}

// announceEngine writes the application version of the engine that holds the lock into the version file, so a joining client reads the version of the live engine. A client never reads a part of the file.
func announceEngine(dir, version string) error {
	if err := atomicfile.Write(filepath.Join(dir, engineVersionName), []byte(version), versionMode); err != nil {
		return fmt.Errorf("write the version into %s: %w", engineVersionName, err)
	}

	return nil
}

// engineVersion reads the version that the engine wrote. It is empty before the engine wrote it and after it stopped. Read it only under probe.lock while an engine holds the engine lock: a crash leaves the file, and the start of the next engine replaces it under probe.lock.
func engineVersion(dir string) (string, error) {
	text, err := os.ReadFile(filepath.Join(dir, engineVersionName)) //nolint:gosec // G304: dir is the state folder of this process and the name is a constant
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}

	if err != nil {
		return "", fmt.Errorf("read the version from %s: %w", engineVersionName, err)
	}

	return string(text), nil
}

// removeEngineVersion deletes the version file. Call it only while holding the engine lock, so no live engine loses its version.
func removeEngineVersion(dir string) error {
	if err := os.Remove(filepath.Join(dir, engineVersionName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", engineVersionName, err)
	}

	return nil
}

// holdProbe takes probe.lock and waits for it. Every holder keeps it for a short time, and a crash frees it.
func holdProbe(dir string) (*flock.Flock, error) {
	probe := lockFile(dir, probeLockName)
	if err := probe.Lock(); err != nil {
		return nil, fmt.Errorf("lock %s: %w", probeLockName, err)
	}

	return probe, nil
}

// holdEngine takes the engine lock without waiting, and returns ErrStateBusy when another process holds it. One engine holds the lock from NewEngine until Shutdown. Call it only under probe.lock.
func holdEngine(dir string) (*flock.Flock, error) {
	held := lockFile(dir, engineLockName)

	taken, err := held.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", engineLockName, err)
	}

	if !taken {
		return nil, ErrStateBusy
	}

	return held, nil
}
