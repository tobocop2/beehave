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

	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/workflow"
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

	h := newHarness(t)
	h.flowIs(t, waitFlow)

	life := &freshLife{fakeLifecycle: h.life, claims: sharedClaims(t, "x")[0]}
	engine := h.engineWith(t, func(e *Engine) { e.deps.Lifecycle = life })
	client := h.client(t, testVersion)

	runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, h))

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
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, replaceRuns(t, client, testItem)[0].Status.Status, "the run of the parent was not canceled")
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
