package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/store"
)

func TestLockKey_ASecondHolderOfOneKeyWaitsAndAnotherKeyDoesNot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	unlock, err := store.LockKey(t.Context(), dir, "foreign:o/r:41")
	require.NoError(t, err)

	other, err := store.LockKey(t.Context(), dir, "foreign:o/r:42")
	require.NoError(t, err, "another key has its own lock file")
	require.NoError(t, other())

	short, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()

	_, err = store.LockKey(short, dir, "foreign:o/r:41")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "take the lock of a shared item key in "+dir)

	require.NoError(t, unlock())

	again, err := store.LockKey(t.Context(), dir, "foreign:o/r:41")
	require.NoError(t, err, "a freed lock can be taken again")
	assert.NoError(t, again())
}
