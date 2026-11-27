package fstree_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/fstree"
)

func TestRemove_RemovesATreeWithAReadOnlyFolderInIt(t *testing.T) {
	t.Parallel()

	for name, readOnly := range map[string]bool{"writable": false, "read_only": true} {
		root := filepath.Join(t.TempDir(), "tree")
		locked := filepath.Join(root, "go", "pkg", "mod", "example.com", "m@v1")
		require.NoError(t, os.MkdirAll(locked, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(locked, "go.mod"), []byte("module m\n"), 0o600))

		if readOnly {
			require.NoError(t, exectest.ChmodFolder(locked, 0o500), "the test makes the folder read-only as the Go command does")
		}

		require.NoError(t, fstree.Remove(root), name)

		_, err := os.Stat(root)
		assert.ErrorIs(t, err, os.ErrNotExist, name)
	}
}

func TestRemove_AMissingTreeIsNoError(t *testing.T) {
	t.Parallel()

	assert.NoError(t, fstree.Remove(filepath.Join(t.TempDir(), "missing")))
}
