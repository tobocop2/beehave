package flow

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
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

	// The run is held at its claim step, so the count is read while the resumed run has not yet replayed the step.
	atClaim := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)

	reached := sync.OnceFunc(func() { close(atClaim) })

	harness.life.onClaim = func() {
		reached()

		hang(release)
	}

	handle := resumeRun(t, engine, client)

	receive(t, atClaim)

	assert.Equal(t, 1, countOf(harness.life.seen(), "claim"), "the resume takes no claim: the claim step of the run runs again")
	unblock()

	_, err = resultOfHandle(t, handle)
	require.NoError(t, err)

	assert.Equal(t, 2, countOf(harness.life.seen(), "claim"), "the claim step ran once on the resume")
	assert.Equal(t, []string{"owner-12#2"}, harness.life.releasedBy(), "the one claim is released")
}

func TestClient_DropClaimGivesNothingBackWhenResumeTookNoClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)

	require.NoError(t, client.dropClaim(testProject, testItem, "run-12", ""))
	assert.Empty(t, harness.life.releasedBy())
}

func TestClient_DropClaimRemovesTheTokenResumeSavedSoNoPhaseWaitsForAReopenThatFailed(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)

	require.NoError(t, saveOwner(client.dir, "run-12", "owner-12"))
	require.NoError(t, client.dropClaim(testProject, testItem, "run-12", "owner-12"))

	saved, err := savedOwner(client.dir, "run-12")
	require.NoError(t, err)
	assert.Empty(t, saved)
	assert.Equal(t, []string{"owner-12"}, harness.life.releasedBy())
}

func TestSavedOwnerAge_ReportsAStateFolderItCannotRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ownersDir), []byte("not a folder"), ownerMode))

	_, found, err := savedOwnerAge(dir, "run-12")

	require.ErrorContains(t, err, "read the claim of run run-12")
	assert.False(t, found)
}

func TestClient_DropClaimStopsWaitingForATrackerThatHangs(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.life.hang = true

	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{Lifecycle: harness.life, Trees: harness.trees, Clock: clock{Release: 50 * time.Millisecond}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	done := make(chan error, 1)

	go func() { done <- client.dropClaim(testProject, testItem, "run-12", "owner-12") }()

	require.ErrorContains(t, receive(t, done), "deadline exceeded")
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

	// The renewal of the fresh claim hangs, so the resumed run is held while the token file is read.
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	harness.beats.hook(func() { receive(t, release) })

	handle := resumeRun(t, engine, client)
	require.FileExists(t, ownerPath(harness.opts.StateDir, testRun), "the resume saves the token")
	unblock()

	_, err = resultOfHandle(t, handle)
	require.NoError(t, err)

	assert.NoFileExists(t, ownerPath(harness.opts.StateDir, testRun), "the release ends the token file")
}

// finishedBeforeNow is the fact that the run finished before the clock of now, so a retention of one nanosecond finds it old.
func finishedBeforeNow(client *Client) waiting[struct{}] {
	return waiting[struct{}]{what: "the finished run to be older than the clock", read: func() (struct{}, bool, error) {
		rows, err := dbos.ListWorkflows(client.flow, dbos.WithFilterWorkflowIDs(testRun), dbos.WithFilterCompletedBefore(time.Now().Add(-time.Nanosecond)))

		return struct{}{}, len(rows) == 1, err
	}}
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
	waitOn(t, finishedBeforeNow(harness.client(t, testVersion)))

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

	_, err = resumeAndJoin(t, first, client)
	require.NoError(t, err)
	require.NoError(t, first.Shutdown())
	waitOn(t, finishedBeforeNow(client))

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
	waitOn(t, condition(func() bool { return harness.agents.running() == 1 }))

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

	waitOn(t, condition(func() bool { return len(harness.life.releasedBy()) == 1 }))
	waitOn(t, condition(func() bool { return !fileExists(cancelPath(harness.opts.StateDir, testRun)) }))
	assert.Equal(t, []string{"owner-12"}, harness.life.releasedBy())
}

func fileExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

func TestLaunch_DropsTheMarkerOfARunThatIsNotCancelled(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	require.NoError(t, markCancel(harness.opts.StateDir, "item-99-r1", cancelMark{ItemInput: ItemInput{Item: "99", Project: testProject}}))

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

	waitOn(t, condition(func() bool { return len(harness.life.releasedBy()) == 1 }))
	waitOn(t, condition(func() bool { return !fileExists(cancelPath(harness.opts.StateDir, testRun)) }))
	assert.FileExists(t, cancelPath(harness.opts.StateDir, "aaa-corrupt"), "the unreadable marker stays for the owner to see")
}

func TestPendingCancels_SkipsAMarkerThatIsNotCompleteAndSaysNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, markCancel(dir, testRun, cancelMark{ItemInput: ItemInput{Item: testItem, Project: testProject}}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cancelsDir, tempPrefix+"123"), []byte(`{"Item"`), ownerMode))

	var out bytes.Buffer

	pending, err := pendingCancels(dir, newLogger(&out))

	require.NoError(t, err)
	assert.Equal(t, map[string]cancelMark{testRun: {ItemInput: ItemInput{Item: testItem, Project: testProject}}}, pending)
	assert.Empty(t, out.String(), "a temporary file is not a marker, so it is not a warning")
}

func TestPendingCancels_LogsAndSkipsAMarkerThatCannotBeRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, markCancel(dir, testRun, cancelMark{ItemInput: ItemInput{Item: testItem, Project: testProject}}))
	require.NoError(t, os.WriteFile(cancelPath(dir, "aaa-corrupt"), nil, ownerMode))

	var out bytes.Buffer

	pending, err := pendingCancels(dir, newLogger(&out))

	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Contains(t, out.String(), "aaa-corrupt")
}

func TestMarkCancel_WritesTheMarkerWithModeOwnerOnly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	require.NoError(t, markCancel(dir, testRun, cancelMark{ItemInput: ItemInput{Item: testItem, Project: testProject}}))

	info, err := os.Stat(cancelPath(dir, testRun))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestMarkCancel_LeavesNoTemporaryFileAndAFolderThatCannotBeWrittenIsAnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, markCancel(dir, testRun, cancelMark{ItemInput: ItemInput{Item: testItem, Project: testProject}}))
	require.NoError(t, os.Mkdir(cancelPath(dir, "blocked"), ownersMode))
	require.Error(t, markCancel(dir, "blocked", cancelMark{}), "the rename onto a folder fails")

	entries, err := os.ReadDir(filepath.Join(dir, cancelsDir))
	require.NoError(t, err)
	require.Len(t, entries, 2, "the marker and the blocking folder stay; no temporary file stays after a failed rename")
	assert.Equal(t, "blocked", entries[0].Name())
	assert.Equal(t, runFile(testRun), entries[1].Name())

	folder := filepath.Join(dir, cancelsDir)

	require.NoError(t, exectest.ChmodFolder(folder, 0o500)) // the test makes its own folder read-only
	t.Cleanup(func() { require.NoError(t, exectest.ChmodFolder(folder, ownersMode)) })

	require.ErrorContains(t, markCancel(dir, "item-13-r1", cancelMark{}), "mark the cancel of run item-13-r1")
}

func TestSweepCancels_DropsTheMarkerOfARunWhenTheItemHasANewerRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	cancelAndStop(t, harness, testItem)

	engine := harness.engine(t)
	waitOn(t, condition(func() bool { return len(harness.life.releasedBy()) == 1 }))

	harness.agents.gate = make(chan struct{})
	t.Cleanup(func() { close(harness.agents.gate) })

	_ = runInBackground(engine, testItem)
	waitOn(t, condition(func() bool { return harness.agents.running() == 1 }))

	require.NoError(t, markCancel(harness.opts.StateDir, testRun, cancelMark{ItemInput: ItemInput{Item: testItem, Project: testProject}}))

	engine.sweepCancels()

	assert.NoFileExists(t, cancelPath(harness.opts.StateDir, testRun))
	assert.Equal(t, []string{"owner-12"}, harness.life.releasedBy(), "the sweep gives nothing back for the older run")
	assert.Equal(t, 1, harness.agents.running(), "the agents of the newer run keep running")
}

func TestSweepCancels_AllTheMarkersShareOneReleaseTimeout(t *testing.T) {
	t.Parallel()

	const (
		// The bound is twice the limit and a separate timeout for each marker costs three times the limit, so the limit is also the room that a busy host has for the sweep itself.
		limit   = 5 * time.Second
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

	waitOn(t, condition(func() bool { return len(harness.life.releasedBy()) == markers }))

	for _, item := range items {
		require.NoError(t, markCancel(harness.opts.StateDir, runID(item, 1), cancelMark{ItemInput: ItemInput{Item: item, Project: testProject}}))
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
	require.ErrorContains(t, markCancel(blocked, testRun, cancelMark{}), "mark the cancel of run "+testRun)

	taken := t.TempDir()
	require.NoError(t, os.MkdirAll(cancelPath(taken, testRun), ownersMode))
	require.ErrorContains(t, markCancel(taken, testRun, cancelMark{}), "mark the cancel of run "+testRun)
}

func TestMarkRunCancel_ATokenFileThatCannotBeReadStopsTheMarker(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(ownerPath(dir, testRun), ownersMode))

	_, err := markRunCancel(nil, dir, testRun, ItemInput{Item: testItem, Project: testProject})

	require.ErrorContains(t, err, "read the claim of run "+testRun)
	assert.NoFileExists(t, cancelPath(dir, testRun))
}

func TestSaveOwner_ReportsAStateFolderThatCannotHoldTheOwnersLock(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "missing")

	require.ErrorContains(t, saveOwner(dir, testRun, testOwner), "lock "+ownersLockName)
}

func TestCancel_RefusesToCancelWhenItCannotWriteTheMarker(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_ = runInBackground(engine, testItem)
	waitOn(t, waiting[struct{}]{what: "the agents of the run to start", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, harness.agents.running() == 1, nil }})
	require.NoError(t, os.WriteFile(filepath.Join(harness.opts.StateDir, cancelsDir), nil, ownerMode))

	require.ErrorContains(t, client.Cancel(testItem), "mark the cancel")

	rows, err := client.Status()
	require.NoError(t, err)
	assert.Equal(t, runRunning, rows[0].State, "a cancel that left no marker does not cancel")

	close(harness.agents.gate)
}

func TestSweepCancels_LeavesTheMarkerWhenTheStoreCannotBeRead(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, markCancel(harness.opts.StateDir, testRun, cancelMark{ItemInput: ItemInput{Item: testItem, Project: testProject}}))
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
	waitOn(t, waiting[struct{}]{what: "the agents of the run to start", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, harness.agents.running() == 1, nil }})
	require.NoError(t, client.Cancel(testItem))

	ticked := make(chan struct{})

	go func() {
		harness.tick(t)
		close(ticked)
	}()

	receive(t, ticked)
	waitOn(t, condition(func() bool { return harness.life.expiries() == 1 }))
}

func TestRun_AReleaseThatHangsEndsTheRunWithAnError(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.life.hang = true
	deps := harness.deps()
	deps.Clock.Release = 100 * time.Millisecond
	engine := engineOn(t, harness, deps)

	done := runInBackground(engine, testItem)

	require.ErrorContains(t, receive(t, done).err, "deadline exceeded")
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

func TestPendingCancels_LogsAndSkipsAMarkerWhoseFileNameIsNotAnEscapedRunId(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, markCancel(dir, testRun, cancelMark{ItemInput: ItemInput{Item: testItem, Project: testProject}}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cancelsDir, "%zz"), nil, ownerMode))

	var out bytes.Buffer

	pending, err := pendingCancels(dir, newLogger(&out))

	require.NoError(t, err)
	assert.Len(t, pending, 1)
	assert.Contains(t, out.String(), "bad file name")
}

func TestMarkRunCancel_TheMarkerWaitsForAProcessThatHoldsTheOwnersLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, saveOwner(dir, testRun, "owner-2"))

	// The test plays an earlier give-back: it holds the owners lock between its compare and its remove of the marker.
	held := lockFile(dir, ownersLockName)
	require.NoError(t, held.Lock())

	done := make(chan error, 1)

	go func() {
		_, err := markRunCancel(nil, dir, testRun, ItemInput{Item: testItem, Project: testProject})
		done <- err
	}()

	soon(t, func() bool { return isClosedErr(done) || waitsForOwnersLock(t.Name()) }, "the cancel waits for the owners lock or writes its marker")
	require.False(t, isClosedErr(done), "the cancel wrote its marker while an earlier give-back held the owners lock")

	require.NoError(t, removeFile(cancelPath(dir, testRun)), "the earlier give-back removes the marker it compared")
	require.NoError(t, held.Unlock())
	require.NoError(t, receive(t, done))

	mark, err := readCancel(dir, testRun)
	require.NoError(t, err, "the earlier give-back removed the marker that the cancel of the resumed run wrote")
	assert.Equal(t, "owner-2", mark.Owner)
}

// isClosedErr reports whether done holds a result. It keeps the result for the next receive.
func isClosedErr(done chan error) bool {
	select {
	case err := <-done:
		done <- err

		return true
	default:
		return false
	}
}

// waitsForOwnersLock reports whether a goroutine that the function creator started waits in the lock of a file while it writes a cancel marker.
func waitsForOwnersLock(creator string) bool {
	for _, stack := range strings.Split(string(goroutineStacks()), "\n\n") {
		if strings.Contains(stack, "/internal/flow."+creator+" in goroutine") && strings.Contains(stack, "markRunCancel") && strings.Contains(stack, "(*Flock).Lock") {
			return true
		}
	}

	return false
}

// goroutineStacks returns the stack of every goroutine of the process. The buffer grows until the dump fits: a dump that fills its buffer is cut, and the stacks that it cuts are missing.
func goroutineStacks() []byte {
	for size := 1 << 20; ; size *= 2 {
		buf := make([]byte, size)

		if n := runtime.Stack(buf, true); n < size {
			return buf[:n]
		}
	}
}

// parkedForStackDump holds a goroutine in a stack of depth+1 frames that the dump of goroutineStacks must show. It calls parked when it holds.
func parkedForStackDump(depth int, parked func(), release <-chan struct{}) {
	if depth > 0 {
		parkedForStackDump(depth-1, parked, release)

		return
	}

	parked()
	<-release
}

func TestGoroutineStacks_ADumpLargerThanTheFirstBufferShowsEveryGoroutine(t *testing.T) {
	t.Parallel()

	const (
		goroutines = 150
		depth      = 60
	)

	release := make(chan struct{})

	var holding sync.WaitGroup

	for range goroutines {
		holding.Add(1)

		go parkedForStackDump(depth, holding.Done, release)
	}

	holding.Wait()

	defer close(release)

	dump := goroutineStacks()

	require.Greater(t, len(dump), 1<<20, "the dump is larger than the first buffer, so the test needs the growth")
	assert.Equal(t, goroutines*(depth+1), bytes.Count(dump, []byte("flow.parkedForStackDump(")), "a cut dump misses the stacks past its end")
}
