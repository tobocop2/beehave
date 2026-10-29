package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.starlark.net/syntax"
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

func TestRun_AMissingFileIsRefusedAndNamesThePath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runners.star")

	err := newLoader(path, runnersSpecs()).run()

	require.ErrorIs(t, err, ErrRefused)
	require.ErrorIs(t, err, fs.ErrNotExist)
	require.ErrorContains(t, err, path)
}

func TestSpecUnpack_RefusesADestinationCountThatDoesNotMatchTheSpec(t *testing.T) {
	t.Parallel()

	var one string

	err := spec{name: "host", args: []string{"provider", "disk_floor_gb?"}}.unpack(syntax.Position{}, nil, &one)

	require.EqualError(t, err, "host: the spec has 2 arguments and the code passed 1 destinations")
}
