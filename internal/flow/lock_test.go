package flow

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEngine_AStartIsNotRefusedByACheckThatHoldsTheEngineLockUnderTheProbeLock(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)

	// A check takes probe.lock, then tries the engine lock, as engineRuns does.
	probe, err := holdProbe(client.dir)
	require.NoError(t, err)

	check, err := holdEngine(client.dir)
	require.NoError(t, err)

	type started struct {
		engine *Engine
		err    error
	}

	result := make(chan started, 1)

	go func() {
		engine, startErr := NewEngine(t.Context(), harness.opts, harness.deps())
		result <- started{engine, startErr}
	}()

	// A start that skips probe.lock meets the held engine lock and returns ErrStateBusy at once. A start that waits for probe.lock stays blocked. The window only lets a wrong start show itself, so it cannot fail a right one.
	select {
	case got := <-result:
		if got.engine != nil {
			require.NoError(t, got.engine.Shutdown())
		}

		require.Fail(t, "the start returned while a check held probe.lock", "error: %v", got.err)
	case <-time.After(300 * time.Millisecond):
	}

	// The check frees the engine lock before probe.lock, as engineRuns does.
	require.NoError(t, check.Unlock())
	require.NoError(t, probe.Unlock())

	got := <-result
	require.NoError(t, got.err, "the start waits for probe.lock, so it never meets the lock of a check")
	require.NoError(t, got.engine.Shutdown())
}

func TestEngineRuns_AHeldEngineLockIsAnEngine(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)

	held, err := holdEngine(client.dir)
	require.NoError(t, err)

	running, err := engineRuns(client.dir)
	require.NoError(t, err)
	assert.True(t, running)
	require.NoError(t, held.Unlock())
}

func TestEngineRuns_AVersionThatACrashedEngineLeftIsNotAnEngine(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)
	require.NoError(t, announceEngine(client.dir, "v-old"))

	running, err := engineRuns(client.dir)
	require.NoError(t, err)
	assert.False(t, running)

	version, err := engineVersion(client.dir)
	require.NoError(t, err)
	assert.Empty(t, version, "the check that took the lock removes the stale version")
}

// blockVersion makes the version file a folder that holds a file, so no read, write or remove of the version file works.
func blockVersion(t *testing.T, dir string) {
	t.Helper()

	folder := filepath.Join(dir, engineVersionName)
	require.NoError(t, os.MkdirAll(folder, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(folder, "x"), nil, 0o600))
}

func TestEngineVersion_AFileThatCannotBeWrittenReadOrRemovedIsReported(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)
	blockVersion(t, client.dir)

	require.ErrorContains(t, announceEngine(client.dir, "v1"), engineVersionName)

	_, err := engineVersion(client.dir)
	require.ErrorContains(t, err, engineVersionName)

	_, err = engineRuns(client.dir)
	require.ErrorContains(t, err, "remove "+engineVersionName, "the check that took the lock cannot remove the file")

	_, err = NewEngine(t.Context(), harness.opts, harness.deps())
	require.ErrorContains(t, err, engineVersionName)

	held, err := holdEngine(client.dir)
	require.NoError(t, err, "a failed start leaves the lock free")

	_, err = engineRuns(client.dir)
	require.ErrorContains(t, err, engineVersionName, "an engine holds the lock and its version cannot be read")

	require.ErrorContains(t, client.sameBuildAsEngine(), engineVersionName)
	require.NoError(t, held.Unlock())
}

func TestEngine_TheVersionIsWrittenWhileTheEngineRunsAndRemovedAtShutdown(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)
	engine := harness.engine(t)

	version, err := engineVersion(client.dir)
	require.NoError(t, err)
	assert.Equal(t, applicationVersion(testVersion), version)

	lockText, err := os.ReadFile(filepath.Join(client.dir, engineLockName))
	require.NoError(t, err)
	assert.Empty(t, lockText, "the lock file stays empty: a rename over it would break the lock")

	require.NoError(t, engine.Shutdown())

	version, err = engineVersion(client.dir)
	require.NoError(t, err)
	assert.Empty(t, version, "a stopped engine leaves no version")
}

func TestNewEngine_ASecondEngineOnOneStateFolderIsRefusedUntilTheFirstStops(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)

	_, err = NewEngine(t.Context(), harness.opts, harness.deps())
	require.ErrorIs(t, err, ErrStateBusy)
	require.EqualError(t, err, "another beehave run owns this state folder; wait for it, or see beehave status")

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

	_, err = engineRuns(harness.client(t, testVersion).dir)
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
	assert.NotErrorIs(t, err, ErrStateBusy)
}

func TestEngineRuns_AHolderThatGivesBackIsNotAnEngine(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)

	// The release of a cancel takes probe.lock, then the engine lock, and holds both for the whole release. No engine runs.
	probe := lockFile(client.dir, "probe.lock")
	require.NoError(t, probe.Lock())

	holder, err := holdEngine(client.dir)
	require.NoError(t, err)

	time.AfterFunc(150*time.Millisecond, func() {
		assert.NoError(t, holder.Unlock())
		assert.NoError(t, probe.Unlock())
	})

	running, err := engineRuns(client.dir)
	require.NoError(t, err)
	assert.False(t, running, "a holder that is not an engine is not an engine, however long it holds")
}

func TestLiveVersion_ANewEngineAfterACrashNeverShowsTheStaleVersion(t *testing.T) {
	t.Parallel()

	client := newHarness(t).client(t, testVersion)
	require.NoError(t, announceEngine(client.dir, "v-crashed"))

	// A new engine is starting: it holds probe.lock and the engine lock, and has not replaced the stale version yet.
	probe := lockFile(client.dir, "probe.lock")
	require.NoError(t, probe.Lock())

	engine, err := holdEngine(client.dir)
	require.NoError(t, err)

	time.AfterFunc(150*time.Millisecond, func() {
		assert.NoError(t, announceEngine(client.dir, "v-new"))
		assert.NoError(t, probe.Unlock())
	})

	live, err := client.liveVersion()
	require.NoError(t, err)
	assert.Equal(t, "v-new", live)
	require.NoError(t, engine.Unlock())
}

func TestLocks_AProbeLockFileThatCannotOpenIsReported(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)
	require.NoError(t, os.MkdirAll(filepath.Join(harness.opts.StateDir, probeLockName), 0o700))

	_, err := engineRuns(client.dir)
	require.ErrorContains(t, err, probeLockName)

	_, err = NewEngine(t.Context(), harness.opts, harness.deps())
	require.ErrorContains(t, err, probeLockName)

	err = client.giveBackUnowned(testProject, testItem, run{})
	require.ErrorContains(t, err, probeLockName)
}

func TestNewEngine_TwoStartsAtOnceMakeOneEngineAndTheOtherIsRefused(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engines := make([]*Engine, 2)
	errs := make([]error, 2)

	var group sync.WaitGroup

	for i := range engines {
		group.Go(func() {
			engines[i], errs[i] = NewEngine(t.Context(), harness.opts, harness.deps())
		})
	}

	group.Wait()

	winner, loser := 0, 1
	if errs[0] != nil {
		winner, loser = 1, 0
	}

	require.NoError(t, errs[winner])
	require.ErrorIs(t, errs[loser], ErrStateBusy)
	require.NoError(t, engines[winner].Shutdown())
}
