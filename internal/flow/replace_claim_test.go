package flow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/claim"
	"github.com/tobocop2/beehest/internal/workflow"
)

// freshLife takes and releases the claim through the claim package over the local store, and mints a new owner token for each claim, as the real lifecycle does.
type freshLife struct {
	*fakeLifecycle
	claims *claim.Claims
	mu     sync.Mutex
	count  int
	taken  int
}

func (l *freshLife) Claim(ctx context.Context, project, item string) (string, error) {
	l.mu.Lock()
	l.count++
	owner := fmt.Sprintf("tok%d", l.count)
	l.mu.Unlock()

	if err := l.claims.Take(ctx, owner, item); err != nil {
		return "", err
	}

	l.mu.Lock()
	l.taken++
	l.mu.Unlock()

	return owner, l.step("claim", project, item)
}

func (l *freshLife) Release(ctx context.Context, project, item, owner string) error {
	return errors.Join(l.claims.Release(ctx, owner, item), l.fakeLifecycle.Release(ctx, project, item, owner))
}

func (l *freshLife) claimsTaken() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.taken
}

// replaceRig starts a running engine over the real claim rows, with an engine tick every few milliseconds, and opens the run of the item with a question.
func replaceRig(t *testing.T) (*harness, *Engine, *Client, *freshLife) {
	t.Helper()

	h, engine, client, life := replaceRigUnticked(t)

	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })

	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(pollEvery):
				select {
				case h.ticks <- time.Time{}:
				case <-stop:
					return
				}
			}
		}
	}()

	return h, engine, client, life
}

// replaceRigUnticked is replaceRig with no engine tick: the engine gives back a canceled run only when the test sends a tick.
func replaceRigUnticked(t *testing.T) (*harness, *Engine, *Client, *freshLife) {
	t.Helper()

	h := newHarness(t)
	h.flowIs(t, waitFlow)

	life := &freshLife{fakeLifecycle: h.life, claims: sharedClaims(t, "x")[0]}
	engine := h.engineWith(t, func(e *Engine) { e.deps.Lifecycle = life })
	client := h.client(t, testVersion)

	runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, h))

	return h, engine, client, life
}

const replaceAncestorFlow = `def leaf(item):
    wait(name = "ask", question = "What now?")

def worker(item):
    runs = spawn(items = ["12"], entry = "leaf", on_open = "replace", name = "up")
    gather(runs, name = "up")

def main(item):
    runs = spawn(items = ["21"], entry = "worker", name = "down")
    gather(runs, name = "down")
`

func TestReplace_ASpawnThatReplacesAnAncestorItemIsRefusedAndCancelsNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)
	client := h.client(t, testVersion)

	refusedAtOnce(t, startFile(engine, testItem, testProject, writeFlow(t, replaceAncestorFlow)))

	child, err := retrieve[ItemResult](client.store(), runID("21", 1))
	require.NoError(t, err)

	_, err = child.GetResult()

	require.Error(t, err, "the child ends with the refusal")
	assert.Contains(t, err.Error(), "ancestor", "the refusal names the problem")

	above, err := engine.parentOf(runID("21", 1))
	require.NoError(t, err)
	assert.Equal(t, runID(testItem, 1), above, "the store names the run that spawned the child")
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, replaceRuns(t, client, testItem)[0].Status.Status, "the run of the parent was not canceled")
}

func TestReplace_AMoveOfARunOfAnotherBuildInsideAReplaceDoesNotTakeTheProbeLockAgain(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	_ = h.engine(t)
	client := h.client(t, otherBuild)
	in := ItemInput{Item: testItem, Project: testProject, OnOpen: workflow.OnOpenReplace}
	plan := attemptPlan{Join: testRun, Move: true}

	done := make(chan error, 1)

	go func() {
		done <- client.underProbe(func() error { return client.moveInStart(t.Context(), plan, in) })
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "the move under the lock of the replace ends")
	case <-time.After(20 * time.Second):
		t.Fatal("the move waits for the lock that the replace holds")
	}

	assert.Equal(t, client.version, notCanceled(t, client).Status.ApplicationVersion, "the run moved to this build")
}

func TestReplace_AMoveForAStartWithNoOptionTakesTheProbeLockItself(t *testing.T) {
	t.Parallel()

	h, _ := otherBuildRun(t)
	_ = h.engine(t)
	client := h.client(t, otherBuild)
	plan := attemptPlan{Join: testRun, Move: true}

	require.NoError(t, client.moveInStart(t.Context(), plan, ItemInput{Item: testItem, Project: testProject}))
	assert.Equal(t, client.version, notCanceled(t, client).Status.ApplicationVersion, "the run moved to this build")
}

func TestReplace_ARunThatSpawnsItsOwnItemOrAnAncestorItemIsRefused(t *testing.T) {
	t.Parallel()

	parents := map[string]string{"item-3-r1": "item-2-r1", "item-2-r1": "item-1-r1", "item-1-r1": ""}
	lookup := func(id string) (string, error) { return parents[id], nil }
	broken := func(string) (string, error) { return "", errors.New("store down") }

	in := ItemInput{Item: "3", Parent: "item-2-r1"}

	require.ErrorContains(t, refuseAncestor(ItemInput{Item: "3"}, "3", lookup), "spawns itself")
	require.ErrorContains(t, refuseAncestor(in, "2", lookup), "ancestor", "the run directly above")
	require.ErrorContains(t, refuseAncestor(in, "1", lookup), "ancestor", "the run two levels above")
	require.NoError(t, refuseAncestor(in, "9", lookup), "an item that no run above executes")
	require.NoError(t, refuseAncestor(ItemInput{Item: "3", Parent: "item-8-r1"}, "9", lookup), "a parent that the store does not know ends the walk")
	require.ErrorContains(t, refuseAncestor(in, "9", broken), "store down", "a store that cannot be read is an error, not a pass")
}

func TestReplace_AGiveBackThatCannotReadTheRunsOfTheItemReleasesNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	closed, err := OpenClient(t.Context(), h.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	a := &attempt{id: runID(testItem, 1), in: ItemInput{Item: testItem, Project: testProject}}

	require.ErrorContains(t, closed.giveOwned(t.Context(), a, "tok"), "list the runs of item 12")
}

func TestReplace_TheParentOfARunIsReadFromTheStoreAndAnUnknownRunHasNone(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	parent, err := engine.parentOf("item-404-r1")

	require.NoError(t, err)
	assert.Empty(t, parent, "no run, no parent")
}

func TestReplace_TwoStartsWithNoOptionAfterTheCancelMakeOneNewRun(t *testing.T) {
	t.Parallel()

	_, engine, client := openRunRig(t)

	last, found, err := latestRun(client.store(), testItem)
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, client.cancelRun(testItem, last))

	startWith(engine, testItem, testProject, workflow.OnOpenJoin)
	startWith(engine, testItem, testProject, workflow.OnOpenJoin)

	soon(t, func() bool { return len(replaceRuns(t, client, testItem)) == 2 }, "attempt 2 started")
	staysTrue(t, func() bool { return len(replaceRuns(t, client, testItem)) == 2 }, "two starts of one attempt id make one run")
}

func TestReplace_AReaderUnderProbeLockNeverSeesTheItemWithNoOpenRunBetweenTheCancelAndTheStart(t *testing.T) {
	t.Parallel()

	_, engine, client := openRunRig(t)

	var (
		mu   sync.Mutex
		gaps []string
	)

	stop := make(chan struct{})
	finished := make(chan struct{})

	go func() {
		defer close(finished)

		for {
			select {
			case <-stop:
				return
			default:
			}

			err := client.underProbe(func() error {
				last, found, err := latestRun(client.store(), testItem)
				if found && last.terminal() {
					mu.Lock()
					gaps = append(gaps, last.Status.ID+"="+string(last.Status.Status))
					mu.Unlock()
				}

				return err
			})
			if err != nil {
				mu.Lock()
				gaps = append(gaps, err.Error())
				mu.Unlock()
			}
		}
	}()

	for attempt := 2; attempt <= 6; attempt++ {
		startWith(engine, testItem, testProject, workflow.OnOpenReplace)

		soon(t, func() bool {
			runs := replaceRuns(t, client, testItem)

			return len(runs) == attempt && !runs[attempt-1].terminal()
		}, "the replace started the next attempt")
	}

	close(stop)
	<-finished

	mu.Lock()
	defer mu.Unlock()

	assert.Empty(t, gaps, "a reader that holds probe.lock saw a newest run that was ended, between the cancel and the start of the next attempt")
}

func TestReplace_TheClaimWaitEndsAtTheReleaseTimeWhenTheMarkerOfTheEarlierAttemptStays(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engineWith(t, func(e *Engine) { e.deps.Clock.Release = 100 * time.Millisecond })

	require.NoError(t, markCancel(engine.dir, runID(testItem, 1), cancelMark{ItemInput: ItemInput{Item: testItem, Project: testProject}}))

	start := time.Now()

	require.NoError(t, engine.awaitRelease(t.Context(), testItem, runID(testItem, 2)))
	assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond, "the wait lasted until the release time")
}

func TestReplace_TheClaimWaitEndsWithTheContextOfTheRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engineWith(t, func(*Engine) {})

	require.NoError(t, markCancel(engine.dir, runID(testItem, 1), cancelMark{ItemInput: ItemInput{Item: testItem, Project: testProject}}))

	ctx, stop := context.WithCancel(t.Context())
	stop()

	require.ErrorIs(t, engine.awaitRelease(ctx, testItem, runID(testItem, 2)), context.Canceled)
}

func TestReplace_TheClaimWaitIgnoresTheMarkerOfAnotherItemAndTheMarkerOfItsOwnRun(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engineWith(t, func(*Engine) {})

	require.NoError(t, markCancel(engine.dir, runID("13", 1), cancelMark{ItemInput: ItemInput{Item: "13", Project: testProject}}))
	require.NoError(t, markCancel(engine.dir, runID(testItem, 2), cancelMark{ItemInput: ItemInput{Item: testItem, Project: testProject}}))

	require.NoError(t, engine.awaitRelease(t.Context(), testItem, runID(testItem, 2)))
}

func TestReplace_TheLateReleaseOfAttemptOneKeepsTheWorktreeOfAttemptTwo(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	h := newHarness(t)
	h.trees.dir = r.tree
	h.flowIs(t, waitFlow)

	engine := h.engine(t)
	client := h.client(t, testVersion)

	runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, h))
	startWith(engine, testItem, testProject, workflow.OnOpenReplace)

	soon(t, func() bool { return len(replaceRuns(t, client, testItem)) == 2 }, "attempt 2 is queued")

	one := &attempt{id: runID(testItem, 1), in: ItemInput{Item: testItem, Project: testProject}, owner: testOwner + testItem}

	require.NoError(t, client.giveBack(one))
	assert.DirExists(t, r.tree, "the release of attempt 1 removed the worktree of the item while attempt 2 is open")
	assert.Equal(t, []string{testOwner + testItem}, h.life.releasedBy(), "the release still gives back the claim of attempt 1")
}

func TestReplace_TheGiveBackOfTheEngineKeepsTheWorktreeOfAttemptTwo(t *testing.T) {
	t.Parallel()

	r := newRepos(t)
	h := newHarness(t)
	h.trees.dir = r.tree
	h.flowIs(t, waitFlow)

	engine := h.engine(t)
	client := h.client(t, testVersion)

	runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, h))
	startWith(engine, testItem, testProject, workflow.OnOpenReplace)

	soon(t, func() bool { return len(replaceRuns(t, client, testItem)) == 2 }, "attempt 2 is queued")

	engine.giveBack(t.Context(), &attempt{id: runID(testItem, 1), in: ItemInput{Item: testItem, Project: testProject}, owner: testOwner + testItem})

	assert.DirExists(t, r.tree, "the give-back of attempt 1 removed the worktree of the item while attempt 2 is open")
	assert.Equal(t, []string{testOwner + testItem}, h.life.releasedBy(), "the give-back still releases the claim of attempt 1")
}

func TestReplace_TheNewAttemptTakesTheClaimAfterTheEngineReleasedTheOldOne(t *testing.T) {
	t.Parallel()

	_, engine, client, life := replaceRig(t)

	startWith(engine, testItem, testProject, workflow.OnOpenReplace)

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 2 && (runs[1].terminal() || life.claimsTaken() == 2)
	}, "attempt 2 took its claim or ended")

	runs := replaceRuns(t, client, testItem)
	require.Len(t, runs, 2, replaceStates(runs))
	assert.False(t, runs[1].terminal(), "attempt 2 is open: %v %v", replaceStates(runs), runs[1].Status.Error)
	assert.Equal(t, 2, life.claimsTaken(), "attempt 2 took the claim that attempt 1 released")
}

// The claim store refuses a take while attempt 1 holds the item, and the engine gives attempt 1 back only at the tick the test sends after attempt 2 started.
func TestReplace_TheNewAttemptWaitsForTheReleaseOfTheCanceledAttemptBeforeItTakesTheClaim(t *testing.T) {
	t.Parallel()

	h, engine, client, life := replaceRigUnticked(t)

	startWith(engine, testItem, testProject, workflow.OnOpenReplace)

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return len(runs) == 2 && runs[1].Status.Status != dbos.WorkflowStatusEnqueued
	}, "attempt 2 started")
	assert.Equal(t, 1, life.claimsTaken(), "attempt 2 takes nothing while attempt 1 holds the claim")

	h.tick(t)

	soon(t, func() bool {
		runs := replaceRuns(t, client, testItem)

		return runs[1].terminal() || life.claimsTaken() == 2
	}, "attempt 2 took its claim or ended")

	runs := replaceRuns(t, client, testItem)
	assert.False(t, runs[1].terminal(), "attempt 2 waited for the release and is open: %v %v", replaceStates(runs), runs[1].Status.Error)
	assert.Equal(t, 2, life.claimsTaken(), "attempt 2 took the claim after the release of attempt 1")
	assert.Equal(t, []string{"tok1"}, life.releasedBy(), "attempt 1 was given back before the take of attempt 2")
}
