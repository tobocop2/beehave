package filelock

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRelease_AnotherHolderCanLockAtOnceWhileAForkedChildStillHoldsACopyOfTheFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "x.lock")

	held, err := Exclusive(path)
	require.NoError(t, err)

	// A child between fork and exec holds a copy of the descriptor. The copy shares the open file, so it shares the lock.
	child, err := syscall.Dup(int(held.file.Fd()))
	require.NoError(t, err)

	t.Cleanup(func() { _ = syscall.Close(child) })

	require.NoError(t, held.Release())

	next, err := TryExclusive(path)
	require.NoError(t, err)
	require.NoError(t, next.Release())
}
