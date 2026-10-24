package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

const stateDirMode os.FileMode = 0o700

// StateDir returns the state folder: OBEE_STATE_DIR (a leading ~ expands), else
// $XDG_STATE_HOME/obee, else ~/.local/state/obee. The path must be absolute; a relative
// XDG_STATE_HOME is ignored, as the XDG specification says.
func StateDir(env map[string]string) (string, error) {
	if dir := env[stateDirVar]; dir != "" {
		return absoluteStateDir(dir)
	}

	if base := env[xdgStateVar]; filepath.IsAbs(base) {
		return filepath.Join(base, stateAppDir), nil
	}

	return homeRelative(filepath.Join(defaultStateRel, stateAppDir))
}

func absoluteStateDir(dir string) (string, error) {
	expanded, err := expandHome(dir)
	if err != nil {
		return "", err
	}

	if !filepath.IsAbs(expanded) {
		return "", refuse("%s=%s is not an absolute path. Write the full path, for example ~/state/obee.", stateDirVar, dir)
	}

	return expanded, nil
}

// EnsureStateDir creates the state folder at mode 0700. It refuses an existing folder, or a
// symlink to one, that another user owns or that the owner cannot read, write and enter, or that
// group or others can use.
func EnsureStateDir(dir string) error {
	return ensureStateDir(dir, os.Getuid())
}

func ensureStateDir(dir string, uid int) error {
	info, err := os.Stat(dir)

	switch {
	case err == nil:
		return checkStateDir(dir, info, uid)
	case errors.Is(err, fs.ErrNotExist):
		if err = os.MkdirAll(dir, stateDirMode); err != nil {
			return refuse("cannot create the state folder %s: %w. Check the parent folder, or set %s.", dir, err, stateDirVar)
		}

		return nil
	default:
		return refuse("cannot read the state folder %s: %w. Check the parent folder, or set %s.", dir, err, stateDirVar)
	}
}

func checkStateDir(dir string, info os.FileInfo, uid int) error {
	if !info.IsDir() {
		return refuse("%s is not a folder. Move it away, or set %s.", dir, stateDirVar)
	}

	// The platform stat type is the only place the owner is recorded.
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != uid {
		return refuse("the state folder %s belongs to another user. Use a folder you own, or set %s.", dir, stateDirVar)
	}

	if mode := info.Mode().Perm(); mode != stateDirMode {
		return refuse("the state folder %s has mode %04o, and obee needs 0700 (owner only). Run: chmod 700 %s", dir, mode, dir)
	}

	return nil
}
