package shell_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/check/shell"
)

func TestRealPath_ARelativeLinkBehindALinkedParentStartsAtTheRealFolder(t *testing.T) {
	t.Parallel()

	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	work := filepath.Join(root, "work")
	require.NoError(t, os.MkdirAll(filepath.Join(work, "a", "b"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join("work", "a", "b"), filepath.Join(root, "s")))
	require.NoError(t, os.Symlink(filepath.Join("..", "..", ".git", "hooks", "x"), filepath.Join(work, "a", "b", "link")))

	assert.Equal(t, filepath.Join(work, ".git", "hooks", "x"), shell.RealPath(filepath.Join(root, "s", "link")))
}

func TestRealPath_ALinkWhoseTargetDoesNotExistYetResolvesToTheTarget(t *testing.T) {
	t.Parallel()

	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(root, ".git", "hooks", "pre-commit"), filepath.Join(root, "abs")))
	require.NoError(t, os.Symlink(filepath.Join(".git", "hooks", "later", "x"), filepath.Join(root, "rel")))
	require.NoError(t, os.Symlink(filepath.Join(root, "loop"), filepath.Join(root, "loop")))

	assert.Equal(t, filepath.Join(root, ".git", "hooks", "pre-commit"), shell.RealPath(filepath.Join(root, "abs")))
	assert.Equal(t, filepath.Join(root, ".git", "hooks", "later", "x"), shell.RealPath(filepath.Join(root, "rel")))
	assert.Equal(t, filepath.Join(root, ".git", "hooks", "pre-commit", "deeper"), shell.RealPath(filepath.Join(root, "abs", "deeper")))
	assert.NotEmpty(t, shell.RealPath(filepath.Join(root, "loop")), "a loop of links ends")
}
