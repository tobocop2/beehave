package flow

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countOf counts the lifecycle calls with the given name for the test item.
func countOf(steps []string, name string) int {
	return len(slices.DeleteFunc(slices.Clone(steps), func(step string) bool { return step != name+":"+testItem }))
}

// shutdownOpeningGate stops the engine and opens the gate of the fake agents once the engine has canceled its runs.
// The fake agent ignores its context, so it stands for the agent process that the engine kills after the cancel.
func shutdownOpeningGate(t *testing.T, engine *Engine, gate chan struct{}) {
	t.Helper()

	stopped := make(chan error, 1)

	go func() { stopped <- engine.Shutdown() }()

	<-engine.ctx.Done()
	close(gate)

	require.NoError(t, <-stopped)
}

func TestCancel_AnEngineThatRunsTheItemReleasesTheClaimOnItsNextTick(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })

	require.NoError(t, client.Cancel(testItem))
	assert.Equal(t, 0, countOf(harness.life.seen(), "release"), "the client leaves the release to the engine that runs the item")

	harness.tick(t)

	select {
	case outcome := <-done:
		require.Error(t, outcome.err)
	case <-time.After(eventTimeout):
		t.Fatal("the run did not end after the cancel")
	}

	eventually(t, func() bool { return countOf(harness.life.seen(), "release") == 1 })
}

func TestCancel_WithNoEngineTheCancelReleasesTheClaimItself(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	harness.migrated(t)

	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	_ = runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })
	shutdownOpeningGate(t, engine, harness.agents.gate)

	client := harness.client(t, testVersion)
	require.Equal(t, 0, countOf(harness.life.seen(), "release"), "a stopped engine leaves the run open and the claim held")

	require.NoError(t, client.Cancel(testItem))

	assert.Equal(t, 1, countOf(harness.life.seen(), "release"))
}

func TestShutdown_AnInterruptLeavesTheRunOpenAndTheNextRunContinuesIt(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	client := harness.client(t, testVersion)

	interrupt, stop := context.WithCancel(t.Context())
	engine, err := NewEngine(interrupt, harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Launch())

	_ = runInBackground(engine, testItem)
	eventually(t, func() bool { return harness.agents.running() == 1 })

	stop() // Ctrl-C cancels the context the command gave the engine.
	shutdownOpeningGate(t, engine, harness.agents.gate)

	infos, err := client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, runRunning, infos[0].State, "the interrupted run is open, not canceled")
	assert.Equal(t, 0, countOf(harness.life.seen(), "release"))

	next := harness.engine(t)
	result, err := next.Run(testItem, testProject)
	require.NoError(t, err)
	assert.Equal(t, ItemResult{Item: testItem}, result)

	infos, err = client.Status()
	require.NoError(t, err)
	require.Len(t, infos, 1, "the next run continues the open run")
	assert.Equal(t, runDone, infos[0].State)
	assert.Equal(t, 1, countOf(harness.life.seen(), "claim"))
	assert.Equal(t, 1, countOf(harness.life.seen(), "release"))
}

func TestGiveBack_LogsAProcessFileAndAReleaseThatFail(t *testing.T) {
	t.Parallel()

	engine := newProcEngine(t, nil)
	engine.procs.path = filepath.Join(t.TempDir(), "missing", pgidFileName)
	engine.deps.Lifecycle = &fakeLifecycle{failing: map[string]bool{"release": true}}
	engine.deps.Trees = fakeTrees{}

	engine.dir = t.TempDir()
	require.NoError(t, saveOwner(engine.dir, "run-12", "tok"))

	engine.giveBack(t.Context(), openRun{id: "run-12", in: ItemInput{Item: testItem, Project: testProject}})
}

func TestGiveBack_AnOwnerThatCannotBeReadKeepsTheClaim(t *testing.T) {
	t.Parallel()

	life := &fakeLifecycle{failing: map[string]bool{}}
	engine := newProcEngine(t, nil)
	engine.deps.Lifecycle = life
	engine.dir = t.TempDir()
	require.NoError(t, os.MkdirAll(ownerPath(engine.dir, "run-12"), ownersMode))

	engine.giveBack(t.Context(), openRun{id: "run-12", in: ItemInput{Item: testItem, Project: testProject}})

	assert.Empty(t, life.seen(), "a claim with no readable token is not released with a guess")
}

func TestGiveBack_ForgetsTheAgentGroupsOfTheCanceledRunOnly(t *testing.T) {
	t.Parallel()

	engine := newProcEngine(t, nil)
	engine.deps.Lifecycle = &fakeLifecycle{failing: map[string]bool{}}
	engine.dir = t.TempDir()

	mine, other := startSleeper(t), startSleeper(t)
	require.NoError(t, engine.procs.add(t.Context(), "run-12", mine))
	require.NoError(t, engine.procs.add(t.Context(), "run-13", other))

	engine.giveBack(t.Context(), openRun{id: "run-12", in: ItemInput{Item: testItem, Project: testProject}})

	assert.Equal(t, []string{"run-13"}, runsOf(engine.procs.rows), "the groups of the canceled run are forgotten, those of another run stay")
	requireStayAlive(t, "the end of the claim context kills a live agent, not the give-back", mine, other)
}

func TestGiveBackWhenEnded_AStopOfTheTickEndsTheWait(t *testing.T) {
	t.Parallel()

	life := &fakeLifecycle{failing: map[string]bool{}}
	engine := newProcEngine(t, nil)
	engine.deps.Lifecycle = life

	stop := make(chan struct{})
	close(stop)

	engine.giveBackWhenEnded(openRun{ended: make(chan struct{})}, stop)

	assert.Empty(t, life.seen())
}

func TestReleaseDeps_GiveBackReportsEachFailureAndKeepsTheOtherStep(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		trees   fakeTrees
		failing map[string]bool
		want    string
	}{
		{"nothing_fails", fakeTrees{}, map[string]bool{}, ""},
		{"the_worktree_lookup_fails", fakeTrees{err: errFake}, map[string]bool{}, "worktree of item 12"},
		{"the_claim_release_fails", fakeTrees{}, map[string]bool{"release": true}, "release of item 12"},
		{"both_fail", fakeTrees{err: errFake}, map[string]bool{"release": true}, "worktree of item 12"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			life := &fakeLifecycle{failing: test.failing}
			deps := ReleaseDeps{Lifecycle: life, Trees: test.trees}

			err := deps.giveBack(t.Context(), slog.New(slog.DiscardHandler), testProject, testItem, "tok")

			if test.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.want)
			}

			assert.Equal(t, []string{"release:" + testItem}, life.seen(), "the claim is released even when the worktree step fails")
		})
	}
}

func TestCancels_EndedIDsListTheFinishedRunsAndForgetDropsThem(t *testing.T) {
	t.Parallel()

	var runs cancels

	finished := make(chan struct{})
	close(finished)
	runs.add("item-12-r1", openRun{ended: finished})
	runs.add("item-13-r1", openRun{ended: make(chan struct{})})

	ended := runs.endedIDs()
	assert.Equal(t, []string{"item-12-r1"}, ended, "a running run is not ended")

	runs.forget(ended)

	assert.Len(t, runs.runs, 1)
	assert.Contains(t, runs.runs, "item-13-r1")
}
