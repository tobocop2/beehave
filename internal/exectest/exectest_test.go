package exectest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/exectest"
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

func TestWrite_AFolderThatDoesNotExistIsAnError(t *testing.T) {
	t.Parallel()

	assert.Error(t, exectest.Write(filepath.Join(t.TempDir(), "missing", "fake"), "#!/bin/sh\n"))
}
