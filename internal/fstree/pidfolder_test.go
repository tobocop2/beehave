package fstree_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/fstree"
)

func TestFolders_MakeMakesAFolderNamedByTheProcessInARootThatItMakes(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "folders")

	made, err := (&fstree.Folders{}).Make(root)

	require.NoError(t, err)
	assert.Equal(t, root, filepath.Dir(made))
	assert.Regexp(t, `^`+strconv.Itoa(os.Getpid())+`-[0-9]+$`, filepath.Base(made))
	assert.DirExists(t, made)
}

func TestFolders_MakeRemovesTheFoldersOfProcessesThatAreGoneAndKeepsTheOnesOfLiveProcesses(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "folders")
	require.NoError(t, os.Mkdir(root, 0o700))

	dead := filepath.Join(root, "999999999-dead")
	readOnly := filepath.Join(dead, "cache")
	require.NoError(t, os.MkdirAll(readOnly, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(readOnly, "f"), nil, 0o600))
	require.NoError(t, exectest.ChmodFolder(readOnly, 0o500))

	live := filepath.Join(root, strconv.Itoa(os.Getppid())+"-live") // the parent process runs, and is not this process
	require.NoError(t, os.Mkdir(live, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(root, "stray"), 0o700))

	made, err := (&fstree.Folders{}).Make(root)
	require.NoError(t, err)

	assert.NoDirExists(t, dead)
	assert.NoDirExists(t, filepath.Join(root, "stray"))
	assert.DirExists(t, live)
	assert.DirExists(t, made)
}

func TestFolders_MakeKeepsTheFoldersThatItMadeAndRemovesTheOnesOfAKilledProcessWithTheSamePid(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "folders")
	folders := &fstree.Folders{}

	first, err := folders.Make(root)
	require.NoError(t, err)

	old := filepath.Join(root, fstree.Name(os.Getpid(), fstree.Attempts-1))
	require.NoError(t, os.Mkdir(old, 0o700))

	second, err := folders.Make(root)
	require.NoError(t, err)

	assert.DirExists(t, first, "a folder that this set made stays")
	assert.DirExists(t, second)
	assert.NoDirExists(t, old, "a folder with the pid of this process that this set did not make is a leftover")
}

func TestFolders_ANilSetKeepsTheFoldersOfThisProcessAndMakesFoldersAllTheSame(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "folders")
	require.NoError(t, os.Mkdir(root, 0o700))

	own := filepath.Join(root, fstree.Name(os.Getpid(), 5))
	require.NoError(t, os.Mkdir(own, 0o700))

	var none *fstree.Folders

	made, err := none.Make(root)
	require.NoError(t, err)

	assert.DirExists(t, own, "a nil set cannot tell its folders from a leftover, so it keeps them")
	assert.DirExists(t, made)
}

func TestFolders_MakeRefusesARootThatIsALink(t *testing.T) {
	t.Parallel()

	target := t.TempDir()
	require.NoError(t, exectest.ChmodFolder(target, 0o700))
	root := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(target, root))

	_, err := (&fstree.Folders{}).Make(root)

	require.ErrorContains(t, err, "not a folder")
	assert.Empty(t, mustList(t, target), "the folder that the link names stays empty")
}

func TestFolders_MakeFailsWhenAllTheNumbersOfTheProcessAreInUse(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "folders")
	require.NoError(t, os.Mkdir(root, 0o700))

	folders := &fstree.Folders{}

	for range fstree.Attempts {
		_, err := folders.Make(root)
		require.NoError(t, err)
	}

	_, err := folders.Make(root)

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

func TestFolders_MakeRefusesARootThatAnotherUserCanWrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, exectest.ChmodFolder(root, 0o777))

	_, err := (&fstree.Folders{}).Make(root)

	require.ErrorContains(t, err, "can be written by another user")
}

func TestFolders_MakeReturnsTheErrorOfARootThatCannotBeMadeListedOrWritten(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("root ignores file modes")
	}

	_, err := (&fstree.Folders{}).Make(filepath.Join(t.TempDir(), "missing", "folders"))
	require.ErrorIs(t, err, fs.ErrNotExist, "the folder above the root is missing")

	root := t.TempDir()
	require.NoError(t, exectest.ChmodFolder(root, 0o300))
	t.Cleanup(func() { assert.NoError(t, exectest.ChmodFolder(root, 0o700)) })

	_, err = (&fstree.Folders{}).Make(root)
	require.ErrorIs(t, err, fs.ErrPermission, "the root cannot be listed")

	require.NoError(t, exectest.ChmodFolder(root, 0o500))

	_, err = (&fstree.Folders{}).Make(root)
	require.ErrorIs(t, err, fs.ErrPermission, "the root cannot be written: the error is the one of the folder, not a count of used numbers")
}
