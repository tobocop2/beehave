package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateFile_NeverReplacesAFileThatAppearsAfterTheCheck(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "workflow.star")
	require.NoError(t, os.WriteFile(path, []byte("mine\n"), 0o600))

	err := createFiles(&bytes.Buffer{}, []initFile{{path: path, data: []byte("init\n"), mode: repoFileMode}})

	require.Error(t, err)

	kept, readErr := os.ReadFile(path) //nolint:gosec // a test reads a file below its own temporary folder
	require.NoError(t, readErr)
	assert.Equal(t, "mine\n", string(kept))
}
