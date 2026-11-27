package exectest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
)

func TestWrite_WritesAProgramThatOnlyItsOwnerCanRunAndThatStarts(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "fake")

	require.NoError(t, exectest.Write(path, "#!/bin/sh\necho started\n"))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	out, err := exec.CommandContext(t.Context(), path).Output() //nolint:gosec // the test starts the program it just wrote
	require.NoError(t, err)
	assert.Equal(t, "started\n", string(out))
}

func TestChmodFolder_ChangesTheModeOfAFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	require.NoError(t, exectest.ChmodFolder(dir, 0o500))
	t.Cleanup(func() { assert.NoError(t, exectest.ChmodFolder(dir, 0o700)) })

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o500), info.Mode().Perm())
}

func TestChmodFolder_AFileOrAMissingPathIsAnError(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	require.ErrorContains(t, exectest.ChmodFolder(file, 0o700), "is not a folder")
	require.Error(t, exectest.ChmodFolder(filepath.Join(t.TempDir(), "missing"), 0o700))

	info, err := os.Stat(file)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a refused path keeps its mode")
}

func TestWrite_AFolderThatDoesNotExistIsAnError(t *testing.T) {
	t.Parallel()

	assert.Error(t, exectest.Write(filepath.Join(t.TempDir(), "missing", "fake"), "#!/bin/sh\n"))
}
