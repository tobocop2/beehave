package fstree

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
)

// foreignInfo is the information of a folder that has no owner that beehave can read.
type foreignInfo struct{ fs.FileInfo }

func (foreignInfo) Sys() any { return nil }

func TestTrusted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, exectest.ChmodFolder(dir, 0o700)) // a host with a group umask makes the folder group-writable

	info, err := os.Lstat(dir)
	require.NoError(t, err)

	require.NoError(t, trusted(dir, info, os.Getuid()))
	require.ErrorContains(t, trusted(dir, info, os.Getuid()+1), "belongs to another user", "a folder of another owner")
	require.ErrorContains(t, trusted(dir, foreignInfo{info}, os.Getuid()), "belongs to another user", "an owner that cannot be read")

	file := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	fileInfo, err := os.Lstat(file)
	require.NoError(t, err)
	require.ErrorContains(t, trusted(file, fileInfo, os.Getuid()), "not a folder")

	require.NoError(t, TrustedRoot(dir, os.Getuid()))
	require.Error(t, TrustedRoot(filepath.Join(dir, "missing"), os.Getuid()), "a folder that is not there")
}

func TestTrusted_AFolderThatAnotherUserCanWriteIsRefused(t *testing.T) {
	t.Parallel()

	for _, mode := range []os.FileMode{0o770, 0o707, 0o777} {
		dir := t.TempDir()
		require.NoError(t, exectest.ChmodFolder(dir, mode))

		require.ErrorContains(t, TrustedRoot(dir, os.Getuid()), "can be written by another user", mode.String())
	}
}

func TestOwnerAlive(t *testing.T) {
	t.Parallel()

	assert.True(t, ownerAlive(strconv.Itoa(os.Getpid())+"-x"))
	assert.True(t, ownerAlive("1-x"), "a process of another owner is alive")
	assert.False(t, ownerAlive("999999999-x"))
	assert.False(t, ownerAlive("stray"))
	assert.False(t, ownerAlive("0-x"))
}
