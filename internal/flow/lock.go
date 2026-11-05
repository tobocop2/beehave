package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

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
	// engineInfoName is the file where the live engine describes itself: its mode, its pid and its start time. It follows the rules of engineVersionName: the engine writes it under probe.lock when it takes the engine lock, a check reads it under probe.lock only while an engine holds the lock, and a check that finds the lock free removes it. The lock decides whether an engine runs, and the file only describes it.
	engineInfoName = "engine.info"
	versionMode    = 0o600

	// ModeRun is the mode of an engine that beehave run starts for one item.
	ModeRun = "run"
	// ModeServe is the mode of the engine of beehave serve, which stays up and takes the items of other commands.
	ModeServe = "serve"
)

// EngineInfo describes the engine that holds the state folder.
type EngineInfo struct {
	// Mode is ModeRun or ModeServe. It is empty while an engine shuts down.
	Mode string `json:"mode"`
	PID  int    `json:"pid"`
	// Since is the time, in UTC, when the engine took the state folder.
	Since time.Time `json:"since"`
}

// liveEngine is what a check learns about the engine that holds the state folder.
type liveEngine struct {
	Version string
	EngineInfo
}

// ErrStateBusy is returned when another engine holds the state folder.
var ErrStateBusy = errors.New("another beehave run owns this state folder; wait for it, or see beehave status")

// lockFile names a lock file in dir.
func lockFile(dir, name string) *flock.Flock {
	return lockfile.Open(filepath.Join(dir, name))
}

// engineState reports whether an engine holds the state folder, and what that engine wrote about itself.
// It holds probe.lock for the whole check, and an engine holds probe.lock while it takes the engine lock and writes its files. So a held engine lock is an engine that has written its files, or a release of giveBackUnowned, which holds probe.lock too and so never meets a check.
// A check that finds the engine lock free removes the files that a stopped engine left, and returns nothing from them.
// The version and the info are empty while an engine shuts down: it removes the files before it frees the lock.
func engineState(dir string) (running bool, live liveEngine, err error) {
	probe, err := holdProbe(dir)
	if err != nil {
		return false, liveEngine{}, err
	}

	defer func() { err = errors.Join(err, probe.Unlock()) }()

	engine, err := holdEngine(dir)
	if errors.Is(err, ErrStateBusy) {
		live, err = readLiveEngine(dir)

		return true, live, err
	}

	if err != nil {
		return false, liveEngine{}, err
	}

	return false, liveEngine{}, errors.Join(removeEngineFiles(dir), engine.Unlock())
}

// readLiveEngine reads the files of the engine that holds the engine lock. Call it only under probe.lock.
func readLiveEngine(dir string) (liveEngine, error) {
	version, err := engineVersion(dir)
	if err != nil {
		return liveEngine{}, err
	}

	info, err := readEngineInfo(dir)

	return liveEngine{Version: version, EngineInfo: info}, err
}

// engineRuns reports whether an engine holds the state folder.
func engineRuns(dir string) (bool, error) {
	running, _, err := engineState(dir)

	return running, err
}

// writeEngineInfo writes the description of the engine that holds the lock. A client never reads a part of the file.
func writeEngineInfo(dir, mode string, pid int, since time.Time) error {
	text, err := json.Marshal(EngineInfo{Mode: mode, PID: pid, Since: since.UTC()})
	if err != nil {
		return fmt.Errorf("encode %s: %w", engineInfoName, err)
	}

	if err := atomicfile.Write(filepath.Join(dir, engineInfoName), text, versionMode); err != nil {
		return fmt.Errorf("write %s: %w", engineInfoName, err)
	}

	return nil
}

// readEngineInfo reads the description that the engine wrote. It is empty before the engine wrote it and after it stopped. Read it only under probe.lock while an engine holds the engine lock, as engineVersion is read.
func readEngineInfo(dir string) (EngineInfo, error) {
	text, err := os.ReadFile(filepath.Join(dir, engineInfoName)) //nolint:gosec // G304: dir is the state folder of this process and the name is a constant
	if errors.Is(err, os.ErrNotExist) {
		return EngineInfo{}, nil
	}

	if err != nil {
		return EngineInfo{}, fmt.Errorf("read %s: %w", engineInfoName, err)
	}

	var info EngineInfo
	if err := json.Unmarshal(text, &info); err != nil {
		return EngineInfo{}, fmt.Errorf("read %s: %w", engineInfoName, err)
	}

	return info, nil
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

// removeEngineFiles deletes the version file and the info file. Call it only while holding the engine lock, so no live engine loses its files.
func removeEngineFiles(dir string) error {
	var failed error

	for _, name := range []string{engineVersionName, engineInfoName} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = errors.Join(failed, fmt.Errorf("remove %s: %w", name, err))
		}
	}

	return failed
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
