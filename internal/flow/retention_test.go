package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// finishedRun runs one item to the end and stops the engine, so the next Launch applies the retention to it.
func finishedRun(t *testing.T, harness *harness) {
	t.Helper()

	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	_, err = first.Run(testItem, testProject)
	require.NoError(t, err)
	require.NoError(t, first.Shutdown())
	time.Sleep(10 * time.Millisecond)
}

func TestLaunch_TheRetentionOfTheOptionsSetsTheAgeAtWhichAFinishedRunIsDeleted(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	finishedRun(t, harness)

	harness.opts.Retention = time.Nanosecond
	launchOn(t, harness, harness.deps())

	rows, err := harness.client(t, testVersion).Status()
	require.NoError(t, err)
	assert.Empty(t, rows, "a run older than the retention of the options is deleted")
}

func TestLaunch_AFinishedRunStaysWhileTheRetentionOfTheOptionsIsLonger(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	finishedRun(t, harness)

	harness.opts.Retention = time.Hour
	launchOn(t, harness, harness.deps())

	rows, err := harness.client(t, testVersion).Status()
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestLaunch_KeepRunsDeletesNothingWhateverTheAge(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	finishedRun(t, harness)

	harness.opts.KeepRuns = true
	deps := harness.deps()
	deps.Clock.Retain = time.Nanosecond
	launchOn(t, harness, deps)

	rows, err := harness.client(t, testVersion).Status()
	require.NoError(t, err)
	assert.Len(t, rows, 1, "a store that keeps its runs deletes none, so item_read of an earlier run and earlier_calls keep their data")
}
