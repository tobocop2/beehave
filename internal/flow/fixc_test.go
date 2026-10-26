package flow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRun    = "item-12-r1"
	testSecret = "sk-ant-abcdefgh12345678"
	hungLimit  = 3 * time.Second
	tickLimit  = 2 * time.Second
)

// engineOn builds and launches an engine over the harness with the given deps. The cleanup shuts it down.
func engineOn(t *testing.T, harness *harness, deps Deps) *Engine {
	t.Helper()

	engine, err := NewEngine(t.Context(), harness.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

func TestResume_ARunThatFailedAtTheClaimStepTakesOneClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.life.failing["claim"] = true
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	harness.life.mu.Lock()
	harness.life.failing["claim"] = false
	harness.life.mu.Unlock()

	require.NoError(t, client.Resume(testItem))
	assert.Equal(t, 1, countOf(harness.life.seen(), "claim"), "the resume takes no claim: the claim step of the run runs again")

	_, err = engine.Run(testItem, testProject)
	require.NoError(t, err)

	assert.Equal(t, 2, countOf(harness.life.seen(), "claim"))
	assert.Equal(t, []string{"owner-12#2"}, harness.life.releasedBy(), "the one claim is released")
}

func TestClient_DropClaimGivesNothingBackWhenResumeTookNoClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)

	require.NoError(t, client.dropClaim(testProject, testItem, ""))
	assert.Empty(t, harness.life.releasedBy())
}

func TestClient_DropClaimStopsWaitingForATrackerThatHangs(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.life.hang = true

	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{Lifecycle: harness.life, Trees: harness.trees, Clock: Clock{Release: 50 * time.Millisecond}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	done := make(chan error, 1)

	go func() { done <- client.dropClaim(testProject, testItem, "owner-12") }()

	select {
	case err := <-done:
		require.ErrorContains(t, err, "deadline exceeded")
	case <-time.After(eventTimeout):
		t.Fatal("the release of the claim has no timeout")
	}
}

func TestFinish_TheTokenFileEndsWithTheRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	require.NoError(t, client.Resume(testItem))
	require.FileExists(t, ownerPath(harness.opts.StateDir, testRun), "the resume saves the token")

	_, err = engine.Run(testItem, testProject)
	require.NoError(t, err)

	assert.NoFileExists(t, ownerPath(harness.opts.StateDir, testRun), "the release ends the token file")
}

func TestLaunch_DeletesTheTokenFileOfAFinishedRunWithTheRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	_, err = first.Run(testItem, testProject)
	require.NoError(t, err)
	require.NoError(t, first.Shutdown())
	require.NoError(t, saveOwner(harness.opts.StateDir, testRun, "owner-12#2"))
	time.Sleep(10 * time.Millisecond)

	deps := harness.deps()
	deps.Clock.Retain = time.Nanosecond
	engineOn(t, harness, deps)

	assert.NoFileExists(t, ownerPath(harness.opts.StateDir, testRun), "a new run with this id must not read the old token")
}

func TestLaunch_AReusedRunIdTakesItsOwnClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	client := harness.client(t, testVersion)
	_, err = first.Run(testItem, testProject)
	require.Error(t, err)

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	require.NoError(t, client.Resume(testItem))
	_, err = first.Run(testItem, testProject)
	require.NoError(t, err)
	require.NoError(t, first.Shutdown())
	time.Sleep(10 * time.Millisecond)

	deps := harness.deps()
	deps.Clock.Retain = time.Nanosecond
	second := engineOn(t, harness, deps)

	_, err = second.Run(testItem, testProject)
	require.NoError(t, err)

	released := harness.life.releasedBy()
	assert.Equal(t, "owner-12#3", released[len(released)-1], "the new run releases its own claim")
}

// cancelAndStop runs an item, cancels it and stops the engine before its tick gives the item back. The cancel marker stays.
func cancelAndStop(t *testing.T, harness *harness, item string) {
	t.Helper()

	harness.agents.gate = make(chan struct{})

	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	client := harness.client(t, testVersion)
	_ = runInBackground(engine, item)
	eventually(t, func() bool { return harness.agents.running() == 1 })

	require.NoError(t, client.Cancel(item))
	require.FileExists(t, cancelPath(harness.opts.StateDir, runID(item, 1)), "the cancel leaves a marker for the release")

	shutdownOpeningGate(t, engine, harness.agents.gate)
}

func TestLaunch_GivesBackARunThatWasCancelledWhileAnEngineStopped(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	cancelAndStop(t, harness, testItem)
	require.Empty(t, harness.life.releasedBy(), "the engine stopped before its tick")

	harness.engine(t)

	eventually(t, func() bool { return len(harness.life.releasedBy()) == 1 })
	eventually(t, func() bool { return !fileExists(cancelPath(harness.opts.StateDir, testRun)) })
	assert.Equal(t, []string{"owner-12"}, harness.life.releasedBy())
}

func fileExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

func TestLaunch_DropsTheMarkerOfARunThatIsNotCancelled(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	require.NoError(t, markCancel(harness.opts.StateDir, "item-99-r1", ItemInput{Item: "99", Project: testProject}))

	harness.engine(t)

	assert.NoFileExists(t, cancelPath(harness.opts.StateDir, "item-99-r1"))
	assert.Empty(t, harness.life.releasedBy(), "a run that is not canceled gives nothing back")
}

func TestLaunch_StartsWhenACancelMarkerCannotBeRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	require.NoError(t, os.MkdirAll(filepath.Join(harness.opts.StateDir, cancelsDir), ownersMode))
	require.NoError(t, os.WriteFile(cancelPath(harness.opts.StateDir, "item-99-r1"), []byte("not json"), ownerMode))

	harness.engine(t)

	assert.Empty(t, harness.life.releasedBy())
	assert.FileExists(t, cancelPath(harness.opts.StateDir, "item-99-r1"), "an unreadable marker stays for the owner to see")
}

func TestLaunch_StartsWhenTheMarkerFolderCannotBeRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	require.NoError(t, os.MkdirAll(harness.opts.StateDir, ownersMode))
	require.NoError(t, os.WriteFile(filepath.Join(harness.opts.StateDir, cancelsDir), nil, ownerMode))

	harness.engine(t)

	assert.Empty(t, harness.life.releasedBy())
}

func TestLaunch_AnUnreadableMarkerDoesNotStopTheSweepOfOtherRuns(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	cancelAndStop(t, harness, testItem)
	require.NoError(t, os.WriteFile(cancelPath(harness.opts.StateDir, "aaa-corrupt"), nil, ownerMode))

	harness.engine(t)

	eventually(t, func() bool { return len(harness.life.releasedBy()) == 1 })
	eventually(t, func() bool { return !fileExists(cancelPath(harness.opts.StateDir, testRun)) })
	assert.FileExists(t, cancelPath(harness.opts.StateDir, "aaa-corrupt"), "the unreadable marker stays for the owner to see")
}

func TestPendingCancels_SkipsAMarkerThatIsNotCompleteAndSaysNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, markCancel(dir, testRun, ItemInput{Item: testItem, Project: testProject}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cancelsDir, tempPrefix+"123"), []byte(`{"Item"`), ownerMode))

	var out bytes.Buffer

	pending, err := pendingCancels(dir, newLogger(&out))

	require.NoError(t, err)
	assert.Equal(t, map[string]ItemInput{testRun: {Item: testItem, Project: testProject}}, pending)
	assert.Empty(t, out.String(), "a temporary file is not a marker, so it is not a warning")
}

func TestPendingCancels_LogsAndSkipsAMarkerThatCannotBeRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, markCancel(dir, testRun, ItemInput{Item: testItem, Project: testProject}))
	require.NoError(t, os.WriteFile(cancelPath(dir, "aaa-corrupt"), nil, ownerMode))

	var out bytes.Buffer

	pending, err := pendingCancels(dir, newLogger(&out))

	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Contains(t, out.String(), "aaa-corrupt")
}

func TestMarkCancel_LeavesNoTemporaryFileAndAFolderThatCannotBeWrittenIsAnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, markCancel(dir, testRun, ItemInput{Item: testItem, Project: testProject}))
	require.Error(t, markCancel(dir, "bad/run", ItemInput{}), "the rename into a missing folder fails")

	entries, err := os.ReadDir(filepath.Join(dir, cancelsDir))
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary file stays after a write or a failed rename")
	assert.Equal(t, testRun, entries[0].Name())

	folder := filepath.Join(dir, cancelsDir)

	require.NoError(t, os.Chmod(folder, 0o500)) //nolint:gosec // the test makes its own folder read-only
	t.Cleanup(func() { require.NoError(t, os.Chmod(folder, ownersMode)) })

	require.ErrorContains(t, markCancel(dir, "item-13-r1", ItemInput{}), "mark the cancel of run item-13-r1")
}

func TestSweepCancels_DropsTheMarkerOfARunWhenTheItemHasANewerRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	cancelAndStop(t, harness, testItem)

	engine := harness.engine(t)
	eventually(t, func() bool { return len(harness.life.releasedBy()) == 1 })

	harness.agents.gate = make(chan struct{})
	t.Cleanup(func() { close(harness.agents.gate) })

	_ = runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })

	require.NoError(t, markCancel(harness.opts.StateDir, testRun, ItemInput{Item: testItem, Project: testProject}))

	engine.sweepCancels()

	assert.NoFileExists(t, cancelPath(harness.opts.StateDir, testRun))
	assert.Equal(t, []string{"owner-12"}, harness.life.releasedBy(), "the sweep gives nothing back for the older run")
	assert.Equal(t, 1, harness.agents.running(), "the agents of the newer run keep running")
}

func TestSweepCancels_AllTheMarkersShareOneReleaseTimeout(t *testing.T) {
	t.Parallel()

	const (
		limit   = 400 * time.Millisecond
		markers = 3
	)

	harness := newHarness(t)
	items := []string{"12", "13", "14"}

	// Each start sweeps the marker of the item before, and the sweep gives it back. The test writes all the markers again below.
	for _, item := range items {
		cancelAndStop(t, harness, item)
	}

	deps := harness.deps()
	deps.Clock.Release = limit
	engine := engineOn(t, harness, deps)

	eventually(t, func() bool { return len(harness.life.releasedBy()) == markers })

	for _, item := range items {
		require.NoError(t, markCancel(harness.opts.StateDir, runID(item, 1), ItemInput{Item: item, Project: testProject}))
	}

	harness.life.mu.Lock()
	harness.life.hang = true
	harness.life.mu.Unlock()

	began := time.Now()

	engine.sweepCancels()

	assert.Less(t, time.Since(began), 2*limit, "one hung tracker delays the start once, not once for each marker")
}

func TestPendingCancels_ReportsAFolderThatCannotBeRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, cancelsDir), nil, ownerMode))

	_, err := pendingCancels(dir, newLogger(&bytes.Buffer{}))

	require.ErrorContains(t, err, "read the cancel markers")
}

func TestForgetDeleted_KeepsTheFilesOfRunsThatCouldNotBeDeleted(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	engine := &Engine{dir: dir, logger: newLogger(&bytes.Buffer{})}
	require.NoError(t, saveOwner(dir, testRun, "owner-12"))

	failure := errors.New("the store refused")

	require.ErrorIs(t, engine.forgetDeleted([]string{testRun}, failure), failure)
	assert.FileExists(t, ownerPath(dir, testRun), "the run is still in the store, so its token file stays")

	require.NoError(t, engine.forgetDeleted([]string{testRun}, nil))
	assert.NoFileExists(t, ownerPath(dir, testRun))
}

func TestMarkCancel_ReportsAFolderAndAFileThatCannotBeWritten(t *testing.T) {
	t.Parallel()

	blocked := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(blocked, cancelsDir), nil, ownerMode))
	require.ErrorContains(t, markCancel(blocked, testRun, ItemInput{}), "mark the cancel of run "+testRun)

	taken := t.TempDir()
	require.NoError(t, os.MkdirAll(cancelPath(taken, testRun), ownersMode))
	require.ErrorContains(t, markCancel(taken, testRun, ItemInput{}), "mark the cancel of run "+testRun)
}

func TestCancel_RefusesToCancelWhenItCannotWriteTheMarker(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_ = runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })
	require.NoError(t, os.WriteFile(filepath.Join(harness.opts.StateDir, cancelsDir), nil, ownerMode))

	require.ErrorContains(t, client.Cancel(testItem), "mark the cancel")

	rows, err := client.Status()
	require.NoError(t, err)
	assert.Equal(t, RunRunning, rows[0].State, "a cancel that left no marker does not cancel")

	close(harness.agents.gate)
}

func TestSweepCancels_LeavesTheMarkerWhenTheStoreCannotBeRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, markCancel(harness.opts.StateDir, testRun, ItemInput{Item: testItem, Project: testProject}))
	require.NoError(t, engine.Shutdown())

	engine.sweepCancels()

	assert.FileExists(t, cancelPath(harness.opts.StateDir, testRun))
	assert.Empty(t, harness.life.releasedBy())
}

func TestForget_LogsAFileThatCannotBeRemoved(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(ownerPath(dir, testRun), "inner"), ownersMode))

	var out bytes.Buffer

	engine := &Engine{dir: dir, logger: newLogger(&out)}
	engine.forget(testRun)

	assert.Contains(t, out.String(), "cannot remove the files of a finished run")
}

func TestTick_AHungReleaseDelaysNeitherTheTickNorTheShutdown(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	harness.life.hang = true
	deps := harness.deps()
	deps.Clock.Release = hungLimit
	engine := engineOn(t, harness, deps)
	client := harness.client(t, testVersion)

	_ = runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })
	require.NoError(t, client.Cancel(testItem))

	ticked := make(chan struct{})

	go func() {
		harness.tick(t)
		close(ticked)
	}()

	select {
	case <-ticked:
	case <-time.After(tickLimit):
		t.Fatal("a release that hangs blocked the tick")
	}

	eventually(t, func() bool { return harness.life.expiries() == 1 })
}

func TestRun_AReleaseThatHangsEndsTheRunWithAnError(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.life.hang = true
	deps := harness.deps()
	deps.Clock.Release = 100 * time.Millisecond
	engine := engineOn(t, harness, deps)

	done := runInBackground(engine, testItem)

	select {
	case outcome := <-done:
		require.ErrorContains(t, outcome.err, "deadline exceeded")
	case <-time.After(eventTimeout):
		t.Fatal("the release step has no timeout")
	}
}

func TestShutdown_ASecondCallReturnsTheFirstResult(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	require.NoError(t, engine.Shutdown())
	require.NoError(t, engine.Shutdown())
}

func TestRegistered_AFailedRegistrationShutsTheEngineDown(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)

	got, err := engine.registered(errFake)

	require.ErrorIs(t, err, errFake)
	assert.Nil(t, got)

	again, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err, "the folder lock is free again")
	require.NoError(t, again.Shutdown())
}

func TestScrub_AStepErrorHoldsNoSecretInTheLogTheStoreOrTheRunError(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.life.failing["claim"] = true
	harness.life.failErr = errors.New("the tracker refused " + testSecret)
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "[redacted]")
	assert.NotContains(t, err.Error(), testSecret)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, testRun)
	require.NoError(t, err)

	var texts []string

	for _, step := range steps {
		if step.Error != nil {
			texts = append(texts, step.Error.Error())
		}
	}

	stored := strings.Join(texts, "\n")

	assert.Contains(t, stored, "[redacted]")
	assert.NotContains(t, stored, testSecret, "DBOS stores the text of the step error")

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterWorkflowIDs(testRun))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.NotContains(t, fmt.Sprint(rows[0].Error), testSecret)

	log, err := os.ReadFile(filepath.Join(harness.opts.StateDir, logFileName))
	require.NoError(t, err)
	assert.NotContains(t, string(log), testSecret)
}

func TestScrub_KeepsTheCauseOfAChangedError(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)

	changed := engine.scrub(fmt.Errorf("refused %s: %w", testSecret, errFake))

	require.ErrorIs(t, changed, errFake)
	assert.NotContains(t, changed.Error(), testSecret)
	require.NoError(t, engine.scrub(nil))
	require.ErrorIs(t, engine.scrub(errFake), errFake)
}
