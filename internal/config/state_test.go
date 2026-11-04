package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
)

func TestStateDir(t *testing.T) { //nolint:paralleltest // sets HOME
	home := homeDir(t)

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"override_wins", map[string]string{"BEEHAVE_STATE_DIR": "/s", "XDG_STATE_HOME": "/x"}, "/s"},
		{"xdg_state_home", map[string]string{"XDG_STATE_HOME": "/x"}, "/x/beehave"},
		{"empty_override_is_unset", map[string]string{"BEEHAVE_STATE_DIR": "", "XDG_STATE_HOME": "/x"}, "/x/beehave"},
		{"default_is_under_the_home_folder", map[string]string{}, filepath.Join(home, ".local", "state", "beehave")},
		{"override_expands_the_home_folder", map[string]string{"BEEHAVE_STATE_DIR": "~/s"}, filepath.Join(home, "s")},
		{"relative_xdg_state_home_is_ignored", map[string]string{"XDG_STATE_HOME": "rel/state"}, filepath.Join(home, ".local", "state", "beehave")},
	}

	for _, tc := range tests { //nolint:paralleltest // sets HOME
		t.Run(tc.name, func(t *testing.T) {
			got, err := config.StateDir(tc.env)

			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestStateDir_RefusesARelativeOverride(t *testing.T) {
	t.Parallel()

	_, err := config.StateDir(map[string]string{"BEEHAVE_STATE_DIR": "rel/state"})

	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorContains(t, err, "BEEHAVE_STATE_DIR=rel/state is not an absolute path")
}

func TestStateDir_RefusesAnOverrideWithTildeAndNoHomeFolder(t *testing.T) {
	t.Setenv("HOME", "")

	_, err := config.StateDir(map[string]string{"BEEHAVE_STATE_DIR": "~/s"})

	require.ErrorIs(t, err, config.ErrRefused)
}

func TestStateDir_RefusesWhenThereIsNoHomeFolder(t *testing.T) {
	t.Setenv("HOME", "")

	_, err := config.StateDir(nil)

	require.ErrorIs(t, err, config.ErrRefused)
}

func TestEnsureStateDir_CreatesTheFolderPrivate(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "a", "beehave")

	require.NoError(t, config.EnsureStateDir(dir))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestEnsureStateDir_AcceptsAnExistingPrivateFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	chmod(t, dir, 0o700)

	require.NoError(t, config.EnsureStateDir(dir))
}

func TestEnsureStateDir_RefusesAFolderTheOwnerCannotUse(t *testing.T) {
	t.Parallel()

	for _, mode := range []os.FileMode{0o000, 0o500, 0o300, 0o600, 0o100} {
		dir := t.TempDir()
		chmod(t, dir, mode)
		t.Cleanup(func() { chmod(t, dir, 0o700) })

		err := config.EnsureStateDir(dir)

		require.ErrorIs(t, err, config.ErrRefused, mode)
		require.ErrorContains(t, err, "Run: chmod 700 "+dir, mode)
	}
}

func TestEnsureStateDir_Symlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	private := filepath.Join(root, "private")
	open := filepath.Join(root, "open")
	require.NoError(t, os.Mkdir(private, 0o700))
	require.NoError(t, os.Mkdir(open, 0o700))
	chmod(t, open, 0o755)

	toPrivate := filepath.Join(root, "to-private")
	toOpen := filepath.Join(root, "to-open")
	dangling := filepath.Join(root, "dangling")
	require.NoError(t, os.Symlink(private, toPrivate))
	require.NoError(t, os.Symlink(open, toOpen))
	require.NoError(t, os.Symlink(filepath.Join(root, "missing"), dangling))

	require.NoError(t, config.EnsureStateDir(toPrivate), "a link to a 0700 folder is accepted")

	err := config.EnsureStateDir(toOpen)
	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorContains(t, err, "mode 0755")

	err = config.EnsureStateDir(dangling)
	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorContains(t, err, "cannot create the state folder")
}

func TestEnsureStateDir_RefusesWiderModesWithTheFix(t *testing.T) {
	t.Parallel()

	for _, mode := range []os.FileMode{0o755, 0o750, 0o701, 0o770, 0o777} {
		dir := t.TempDir()
		chmod(t, dir, mode)

		err := config.EnsureStateDir(dir)

		require.ErrorIs(t, err, config.ErrRefused, mode)
		require.ErrorContains(t, err, "Run: chmod 700 "+dir, mode)
	}
}

func TestEnsureStateDir_RefusesAFile(t *testing.T) {
	t.Parallel()

	path := writeFile(t, filepath.Join(t.TempDir(), "state"), "x")

	err := config.EnsureStateDir(path)

	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorContains(t, err, "is not a folder")
}

func TestEnsureStateDir_RefusesAFolderBehindAClosedParent(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	chmod(t, parent, 0o000)
	t.Cleanup(func() { chmod(t, parent, 0o700) })

	err := config.EnsureStateDir(filepath.Join(parent, "beehave"))

	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorContains(t, err, "cannot read the state folder")
}

func TestEnsureStateDir_RefusesAFolderItCannotCreate(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	chmod(t, parent, 0o500)

	err := config.EnsureStateDir(filepath.Join(parent, "beehave"))

	require.ErrorIs(t, err, config.ErrRefused)
	require.ErrorContains(t, err, "BEEHAVE_STATE_DIR")
}
