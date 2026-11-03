package srt

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

func TestWrap_ATemporaryFolderThatDoesNotExistReturnsTheError(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "gone"))

	_, _, err := Wrap(agent.SandboxPlan{}, "/home/o", "true")

	require.ErrorContains(t, err, "write the sandbox settings")
}

func TestWrap_AFailedWriteRemovesTheFileAndReturnsTheError(t *testing.T) {
	t.Parallel()

	var name string

	failing := func(path string, _ []byte, _ os.FileMode) error {
		name = path

		return errors.New("disk full")
	}

	args, cleanup, err := wrap(agent.SandboxPlan{}, "/home/o", "true", failing)

	require.ErrorContains(t, err, "write the sandbox settings: disk full")
	assert.Nil(t, args)
	assert.Nil(t, cleanup)

	_, statErr := os.Stat(name)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}
