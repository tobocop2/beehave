package flow

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// commandLineClient opens the client of a command-line process of one build, over a lifecycle that the test chose.
func commandLineClient(t *testing.T, h *harness, version string, life lifecycle) *Client {
	t.Helper()

	opts := h.opts
	opts.Version = version

	client, err := OpenClient(t.Context(), opts, ReleaseDeps{Lifecycle: life, Beats: h.beats, Trees: h.trees, Clock: clock{Release: time.Second}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	return client
}

// resumedAndCanceledRig fails the first run of the item in an engine that then stops, starts the serve engine, and moves that run to another build and cancels it,
// both from a command line with its own owner, while the serve engine holds the state folder. The serve engine never executed the run.
func resumedAndCanceledRig(t *testing.T) (harness *harness, serve *Engine) {
	t.Helper()

	harness = newHarness(t)
	harness.agents.err = errFake

	claims := sharedClaims(t, "first", "serve", "cli")
	cliLife := &claimingLife{fakeLifecycle: &fakeLifecycle{failing: map[string]bool{}}, claims: claims[2], owner: "cli"}

	first := harness.engineWith(t, func(e *Engine) {
		e.deps.Lifecycle = &claimingLife{fakeLifecycle: harness.life, claims: claims[0], owner: "first"}
	})

	_, err := first.Run(testItem, testProject)
	require.Error(t, err, "the first run fails at the agent")
	require.NoError(t, first.Shutdown())

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	serve = harness.engineWith(t, func(e *Engine) {
		e.deps.Lifecycle = &claimingLife{fakeLifecycle: harness.life, claims: claims[1], owner: "serve"}
	})

	cli := commandLineClient(t, harness, otherBuild, cliLife)
	require.NoError(t, cli.ResumeOnThisBuild(testItem))
	require.NoError(t, cli.CancelRun(testRun), "the cancel leaves the release to the engine that holds the state folder")
	require.FileExists(t, cancelPath(serve.dir, testRun), "the engine has not released the run yet")

	return harness, serve
}

func TestCancel_TheNextRunOfAnotherOwnerTakesTheItemAfterAResumeAndACancelFromTheCommandLine(t *testing.T) {
	t.Parallel()

	harness, serve := resumedAndCanceledRig(t)

	harness.tick(t)
	soon(t, func() bool { return !fileExists(cancelPath(serve.dir, testRun)) }, "the tick gives back the canceled run that the engine does not execute")

	result, err := serve.Run(testItem, testProject)

	require.NoError(t, err, "the claim of the resume does not refuse the next run")
	assert.Equal(t, ItemResult{Item: testItem}, result)
}

// soon waits a short time for a fact that a tick or a give-back brings at once, so a regression fails in seconds and not at the bound of waitOn.
func soon(t *testing.T, check func() bool, what string) {
	t.Helper()

	require.Eventually(t, check, soonBound, pollEvery, what)
}

const soonBound = 20 * time.Second

func TestCancel_AMarkerOfARunThatIsOpenAndNotCanceledYetStaysForTheCancelThatIsAboutToCome(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)

	runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	in := ItemInput{Item: testItem, Project: testProject}
	require.NoError(t, markCancel(engine.dir, testRun, cancelMark{ItemInput: in}))

	engine.giveBackStray(&attempt{id: testRun, in: in})

	assert.FileExists(t, cancelPath(engine.dir, testRun), "the run is open, so the cancel may still come")
	assert.Empty(t, harness.life.releasedBy())
}

func TestCancel_AMarkerOfARunThatEndedWithoutACancelIsDropped(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	in := ItemInput{Item: testItem, Project: testProject}
	require.NoError(t, markCancel(engine.dir, testRun, cancelMark{ItemInput: in}))

	engine.giveBackStray(&attempt{id: testRun, in: in})

	assert.NoFileExists(t, cancelPath(engine.dir, testRun), "the run ended, so no cancel comes and the tick reads the store for it no more")
}

func TestCancel_TheReleaseRecordOfACanceledLocalRunEndsWithItsGiveBack(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, waiting[struct{}]{what: "the agent to start", run: runOfEngine(t, engine), read: func() (struct{}, bool, error) { return struct{}{}, harness.agents.running() == 1, nil }})

	require.NoError(t, client.Cancel(testItem))
	harness.tick(t)
	require.Error(t, receive(t, done).err)
	soon(t, func() bool { return countOf(harness.life.seen(), "release") == 1 }, "the give-back of the canceled run releases the item")

	soon(t, func() bool { return engine.cancels.begin(&attempt{id: testRun}) }, "a later begin of the same run id is refused for ever when the give-back leaves its record")
}

func TestCancels_BeginRefusesARunThatThisProcessExecutesOrReleases(t *testing.T) {
	t.Parallel()

	var list cancels

	local := &attempt{id: "local", cancel: func() {}}
	list.add(local)

	assert.False(t, list.begin(&attempt{id: "local"}), "a local run is released by its own give-back")

	stray := &attempt{id: "stray"}
	assert.True(t, list.begin(stray))
	assert.False(t, list.begin(&attempt{id: "stray"}), "a release under way is not started twice")

	list.finish(stray)

	assert.True(t, list.begin(&attempt{id: "stray"}))

	cut := list.cancel([]*attempt{local})

	require.Len(t, cut, 1)
	assert.False(t, list.begin(&attempt{id: "local"}), "a cut run is released until its give-back ends")

	gate := &attempt{id: "gate", cancel: func() {}, gate: true}
	list.add(gate)
	require.Len(t, list.cancel([]*attempt{gate}), 1)
	assert.True(t, list.begin(&attempt{id: "gate"}), "a gate has no give-back, so a cut gate is not held")
}

func TestCancel_AnUnreadableCancelFolderIsLoggedAndTheTickGoesOn(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, cancelsDir), nil, ownerMode))

	engine := &Engine{dir: dir, logger: newLogger(&logged, runRedactor(nil))}

	engine.giveBackStrayCancels()

	assert.Contains(t, logged.String(), "cannot read the canceled runs")
}
