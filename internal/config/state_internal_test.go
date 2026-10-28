package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEnsureStateDir_RefusesAFolderAnotherUserOwns(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(dir, 0o700))

	err := ensureStateDir(dir, os.Getuid()+1)

	require.ErrorIs(t, err, ErrRefused)
	require.ErrorContains(t, err, "belongs to another user")
	require.NoError(t, ensureStateDir(dir, os.Getuid()))
}

func TestDecodeFile_AMissingFileIsRefusedAndNamesThePath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runners.hcl")

	err := decodeFile(path, &rawRunnersFile{})

	require.ErrorIs(t, err, ErrRefused)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.ErrorContains(t, err, path)
}
