package fstree_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/fstree"
)

func TestPidFolder_MakesAFolderNamedByTheProcessInARootThatItMakes(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "folders")

	made, err := fstree.PidFolder(root)

	require.NoError(t, err)
	assert.Equal(t, root, filepath.Dir(made))
	assert.Regexp(t, `^`+strconv.Itoa(os.Getpid())+`-[0-9]+$`, filepath.Base(made))
	assert.DirExists(t, made)
}

func TestPidFolder_RemovesTheFoldersOfProcessesThatAreGoneAndKeepsTheOnesOfLiveProcesses(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "folders")
	require.NoError(t, os.Mkdir(root, 0o700))

	dead := filepath.Join(root, "999999999-dead")
	readOnly := filepath.Join(dead, "cache")
	require.NoError(t, os.MkdirAll(readOnly, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(readOnly, "f"), nil, 0o600))
	require.NoError(t, exectest.ChmodFolder(readOnly, 0o500))

	live := filepath.Join(root, strconv.Itoa(os.Getpid())+"-live")
	require.NoError(t, os.Mkdir(live, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "stray"), 0o700))

	made, err := fstree.PidFolder(root)
	require.NoError(t, err)

	assert.NoDirExists(t, dead)
	assert.NoDirExists(t, filepath.Join(root, "stray"))
	assert.DirExists(t, live)
	assert.DirExists(t, made)
}

func TestPidFolder_ARootThatIsALinkIsRefused(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	require.NoError(t, exectest.ChmodFolder(target, 0o700))
	root := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(target, root))

	_, err := fstree.PidFolder(root)

	require.ErrorContains(t, err, "not a folder")
	assert.Empty(t, mustList(t, target), "the folder that the link names stays empty")
}

func TestPidFolder_ARootWhoseNumbersAreAllInUseIsAnError(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "folders")
	require.NoError(t, os.Mkdir(root, 0o700))

	for range fstree.Attempts {
		_, err := fstree.PidFolder(root)
		require.NoError(t, err)
	}

	_, err := fstree.PidFolder(root)

	require.ErrorContains(t, err, "folders of this process already")
}

func TestName_JoinsThePidAndTheNumber(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "4194304-99", fstree.Name(4194304, 99))
}

func mustList(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}

func TestPidFolder_ARootThatAnotherUserCanWriteIsRefused(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, exectest.ChmodFolder(root, 0o777))

	_, err := fstree.PidFolder(root)

	require.ErrorContains(t, err, "can be written by another user")
}

func TestPidFolder_ARootThatCannotBeMadeOrListedIsAnError(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("root ignores file modes")
	}

	_, err := fstree.PidFolder(filepath.Join(t.TempDir(), "missing", "folders"))
	require.Error(t, err, "the folder above the root is missing")

	root := t.TempDir()
	require.NoError(t, exectest.ChmodFolder(root, 0o300))
	t.Cleanup(func() { assert.NoError(t, exectest.ChmodFolder(root, 0o700)) })

	_, err = fstree.PidFolder(root)
	require.Error(t, err, "the root cannot be listed")

	require.NoError(t, exectest.ChmodFolder(root, 0o500))

	_, err = fstree.PidFolder(root)
	require.Error(t, err, "the root cannot be written")
}
