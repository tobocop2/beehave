package credential

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOwned_ARefusesAPathThatDoesNotExist(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, owned(filepath.Join(t.TempDir(), "gone"), fileMode, false, os.Getuid()), "the credential path")
}

func TestOwned_ARefusesAFileThatAnotherUserOwns(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "w.token")
	require.NoError(t, os.WriteFile(path, []byte("tok"), fileMode))

	require.NoError(t, owned(path, fileMode, false, os.Getuid()))
	require.ErrorContains(t, owned(path, fileMode, false, os.Getuid()+1), "belongs to another user")
}
