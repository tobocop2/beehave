package config

import (
	"errors"
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

func TestDescribeDecodeError_NamesTheFileWhenNoKeyIsKnown(t *testing.T) {
	t.Parallel()

	cause := errors.New("boom")
	err := describeDecodeError("runners.toml", cause, "name")

	require.ErrorIs(t, err, ErrRefused)
	require.ErrorIs(t, err, cause)
	require.ErrorContains(t, err, "runners.toml cannot be read: boom")
}
