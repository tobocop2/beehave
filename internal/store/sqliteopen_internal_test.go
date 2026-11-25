package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })

	return root
}

func TestSecure_SkipsAFileThatIsNotCreatedAndIsGone(t *testing.T) {
	t.Parallel()

	require.NoError(t, secure(openTestRoot(t, t.TempDir()), "gone.db-shm", false))
}

func TestSecure_RefusesAFileThatIsToBeCreatedWhenItsFolderIsGone(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, secure(openTestRoot(t, t.TempDir()), "missing/file.db", true), fs.ErrNotExist)
}

func TestSecure_KeepsAnErrorThatIsNotAMissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain"), nil, 0o600))

	err := secure(openTestRoot(t, dir), "plain/inside-shm", false)
	require.ErrorIs(t, err, syscall.ENOTDIR)
}
