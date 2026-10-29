package flow

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEngine_ASecondEngineOnOneStateFolderIsRefusedUntilTheFirstStops(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)

	_, err = NewEngine(t.Context(), harness.opts, harness.deps())
	require.ErrorIs(t, err, errStateBusy)
	require.EqualError(t, err, "another obee run owns this state folder; wait for it, or see obee status")

	client := harness.client(t, testVersion)
	_, err = client.Status()
	require.NoError(t, err, "the client commands never take the engine lock")

	require.NoError(t, first.Shutdown())

	// A process that another test forks holds a copy of the lock file until it runs exec, so the lock can outlive Shutdown by a moment.
	var second *Engine

	eventually(t, func() bool {
		var openErr error

		second, openErr = NewEngine(t.Context(), harness.opts, harness.deps())

		return openErr == nil
	})
	require.NoError(t, second.Shutdown())
}

func TestOpenClient_ManyFirstOpensOfANewStateFolderAllSucceed(t *testing.T) {
	t.Parallel()

	const (
		folders = 3
		opens   = 8
	)

	for range folders {
		harness := newHarness(t)
		errs := make([]error, opens)
		clients := make([]*Client, opens)

		var group sync.WaitGroup

		for i := range opens {
			group.Go(func() { clients[i], errs[i] = OpenClient(t.Context(), harness.opts, ReleaseDeps{}) })
		}

		group.Wait()

		for i := range opens {
			require.NoError(t, errs[i])
			require.NoError(t, clients[i].Close())
		}
	}
}

func TestLocks_AnEngineLockFileThatCannotOpenIsReported(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	require.NoError(t, os.MkdirAll(filepath.Join(harness.opts.StateDir, engineLockName), 0o700))

	_, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.ErrorContains(t, err, engineLockName)
}

func TestCancel_ReportsARunWhoseInputIsNotReadable(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)

	_, err := client.markCancel(testItem, run{Status: dbos.WorkflowStatus{ID: "item-12-r1", Input: "not json"}})

	require.ErrorContains(t, err, "read the input of run item-12-r1")
}

func TestCancel_ReportsAnEngineLockThatCannotOpen(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)
	require.NoError(t, os.MkdirAll(filepath.Join(harness.opts.StateDir, engineLockName), 0o700))

	err := client.giveBackUnowned(testProject, testItem, run{})

	require.ErrorContains(t, err, engineLockName)
	assert.NotErrorIs(t, err, errStateBusy)
}
