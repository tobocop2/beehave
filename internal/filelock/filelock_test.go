package filelock_test

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/filelock"
)

func lockPath(t *testing.T) string {
	t.Helper()

	return filepath.Join(t.TempDir(), "x.lock")
}

func TestExclusive_ASecondTryFailsWhileTheFirstHoldsAndPassesAfterRelease(t *testing.T) {
	t.Parallel()

	path := lockPath(t)

	held, err := filelock.Exclusive(path)
	require.NoError(t, err)

	_, err = filelock.TryExclusive(path)
	require.ErrorIs(t, err, syscall.EWOULDBLOCK)

	require.NoError(t, held.Release())

	again, err := filelock.TryExclusive(path)
	require.NoError(t, err)
	require.NoError(t, again.Release())
}

func TestShared_SharersRunTogetherAndAHolderExcludesThem(t *testing.T) {
	t.Parallel()

	path := lockPath(t)

	one, err := filelock.Shared(path)
	require.NoError(t, err)

	second := make(chan *filelock.Lock, 1)

	go func() {
		two, secondErr := filelock.Shared(path)
		assert.NoError(t, secondErr)

		second <- two
	}()

	two := within(t, second, "a second sharer does not wait for the first")

	_, err = filelock.TryExclusive(path)
	require.ErrorIs(t, err, syscall.EWOULDBLOCK, "a holder cannot take the lock while a sharer has it")

	require.NoError(t, one.Release())
	require.NoError(t, two.Release())
}

// within returns what arrives on the channel and fails the test when nothing arrives in two seconds.
func within[T any](t *testing.T, got <-chan T, what string) T {
	t.Helper()

	select {
	case value := <-got:
		return value
	case <-time.After(2 * time.Second):
		require.FailNow(t, "timed out after 2 s: "+what)
	}

	var none T

	return none
}

func TestExclusive_ASecondHolderWaitsWhileTheFirstHolds(t *testing.T) {
	t.Parallel()

	path := lockPath(t)

	held, err := filelock.Exclusive(path)
	require.NoError(t, err)

	second := make(chan *filelock.Lock, 1)

	go func() {
		two, secondErr := filelock.Exclusive(path)
		assert.NoError(t, secondErr)

		second <- two
	}()

	select {
	case <-second:
		require.FailNow(t, "a second Exclusive got the lock while the first held it")
	case <-time.After(150 * time.Millisecond):
	}

	require.NoError(t, held.Release())
	require.NoError(t, within(t, second, "the second holder after the release").Release())
}

func TestExclusive_WaitsForTheHolderSoOnlyOneRunsAtATime(t *testing.T) {
	t.Parallel()

	const workers = 20

	path := lockPath(t)

	var (
		inside, overlaps atomic.Int32
		group            sync.WaitGroup
	)

	for range workers {
		group.Go(func() {
			held, err := filelock.Exclusive(path)
			if err != nil {
				overlaps.Add(workers)

				return
			}

			if inside.Add(1) != 1 {
				overlaps.Add(1)
			}

			inside.Add(-1)

			if err := held.Release(); err != nil {
				overlaps.Add(workers)
			}
		})
	}

	group.Wait()
	assert.Zero(t, overlaps.Load())
}

func TestLock_AFileThatCannotOpenIsAnErrorThatNamesIt(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no-such-folder", "x.lock")

	for name, take := range map[string]func(string) (*filelock.Lock, error){
		"Exclusive": filelock.Exclusive, "TryExclusive": filelock.TryExclusive, "Shared": filelock.Shared,
	} {
		_, err := take(missing)
		require.ErrorIs(t, err, os.ErrNotExist, name)
		assert.ErrorContains(t, err, missing, name)
	}
}

func TestLock_TheFileHasMode0600(t *testing.T) {
	t.Parallel()

	path := lockPath(t)

	held, err := filelock.Exclusive(path)
	require.NoError(t, err)
	require.NoError(t, held.Release())

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestRelease_ASecondReleaseReportsTheClosedFile(t *testing.T) {
	t.Parallel()

	held, err := filelock.Exclusive(lockPath(t))
	require.NoError(t, err)
	require.NoError(t, held.Release())
	require.Error(t, held.Release())
}
