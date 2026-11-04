package atomicfile_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/atomicfile"
)

func TestWriteSetsTheModeOfANewAndOfAnExistingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "file")

	require.NoError(t, atomicfile.Write(path, []byte("one"), 0o600))
	assertFile(t, path, "one")

	require.NoError(t, os.Chmod(path, 0o644)) //nolint:gosec // the test makes the mode that Write must reset
	require.NoError(t, atomicfile.Write(path, []byte("two"), 0o600))
	assertFile(t, path, "two")
}

func TestWriteFailsWhenTheFolderIsMissing(t *testing.T) {
	t.Parallel()

	require.Error(t, atomicfile.Write(filepath.Join(t.TempDir(), "none", "file"), nil, 0o600))
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // the test reads a file it just made in its own temp folder
	require.NoError(t, err)
	assert.Equal(t, want, string(data))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
