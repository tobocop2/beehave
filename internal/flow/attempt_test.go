package flow

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resumeInsideBeat arms a hook that, inside the first renewal of the tick, stops the first attempt, takes its outcome from done into first, resumes the run and waits for the resumed claim to be trusted.
// It returns the claim row of the resumed run as the hook read it, and a channel that gets the row as the next renewal of the tick finds it:
// that tick began after the answer of the earlier attempt, and its own renewal of the resumed claim has not changed the row yet.
func resumeInsideBeat(t *testing.T, harness *harness, engine *Engine, client *Client, done <-chan runOutcome, first *runOutcome) (*claimHold, <-chan claimHold) {
	t.Helper()

	earlier := endedOf(engine, testRun)

	var armed, resumedOnce atomic.Bool

	resumed := &claimHold{}
	next := make(chan claimHold, 1)

	harness.beats.hook(func() {
		if resumedOnce.CompareAndSwap(true, false) {
			next <- claimRowOf(engine, testRun)

			return
		}

		if !armed.CompareAndSwap(true, false) {
			return
		}

		*first = endFirstAttempt(t, engine, done)

		assert.Eventually(t, func() bool { open, err := engine.runOpen(testRun); return err == nil && !open }, soonBound, pollEvery, "the store records the end of the first attempt")

		resumeAndList(t, engine, client, earlier)

		assert.Eventually(t, func() bool {
			_, _, reason, early := engine.holds.distrust(testRun, engine.deps.Clock)

			return countOf(harness.life.seen(), "claim") == 2 && reason == "" && !early
		}, soonBound, pollEvery, "the resumed run holds a trusted claim")

		*resumed = claimRowOf(engine, testRun)
		resumedOnce.Store(true)
	})
	armed.Store(true)

	return resumed, next
}

func TestAttempt_ALossOfTheEarlierAttemptCancelsThatAttemptAndNotTheResumedOne(t *testing.T) {
	t.Parallel()

	engine := &Engine{}

	var earlierStopped, resumedStopped atomic.Bool

	earlier := &attempt{id: testRun, cancel: func() { earlierStopped.Store(true) }}
	resumed := &attempt{id: testRun, cancel: func() { resumedStopped.Store(true) }}

	// The earlier attempt still unwinds with its claim row while the resumed attempt is listed under the same run id.
	engine.holds.add(earlier, claimHold{renewed: time.Now()})
	engine.cancels.add(resumed)

	engine.lose(earlier, "the tracker said the claim is gone")

	assert.True(t, earlierStopped.Load(), "the loss cancels the attempt that held the claim")
	assert.False(t, resumedStopped.Load(), "the loss of the earlier attempt canceled the resumed attempt of the same run id")
}

func TestAttempt_TheFirstRenewalOfTheEarlierAttemptLeavesTheClaimOfTheResumedOne(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)

	earlier := &attempt{id: testRun, in: ItemInput{Item: testItem, Project: testProject}}
	engine.holds.add(earlier, claimHold{project: testProject, owner: testOwner})

	// The run resumes while the tracker answers the first renewal of the earlier attempt.
	var resumed *attempt

	harness.beats.hook(func() { resumed = holdClaim(engine, testRun, claimHold{project: testProject, owner: testOwner + "#2"}) })

	require.NoError(t, engine.firstRenewal(t.Context(), earlier, testOwner))

	require.NotNil(t, resumed)
	assert.Same(t, resumed, claimAttempt(engine, testRun))
	assert.True(t, claimRowOf(engine, testRun).renewed.IsZero(), "the first renewal of the earlier attempt trusted the claim of the resumed attempt")
}

func TestAttempt_TheEndOfAPhaseOfTheEarlierAttemptKeepsTheAgentGroupsOfTheResumedOne(t *testing.T) {
	t.Parallel()

	agents := blockingAgents{pid: make(chan int, 1), proceed: make(chan struct{})}
	engine := newProcEngine(t, agents)

	done := make(chan error, 1)

	go func() {
		_, err := runAgentOf(t.Context(), engine, PhaseInput{Run: procRun, Item: testItem})
		done <- err
	}()

	receive(t, agents.pid)

	// The run resumes: the resumed attempt holds the claim, and its agent starts before the phase of the earlier attempt ends.
	resumed := holdClaim(engine, procRun, claimHold{renewed: time.Now()})
	theirs := startSleeper(t)
	require.NoError(t, engine.procs.add(t.Context(), resumed, theirs))

	close(agents.proceed)
	require.NoError(t, receive(t, done))

	require.Len(t, engine.procs.rows, 1, "the end of the phase of the earlier attempt forgot the agent groups of the resumed attempt")
	assert.Same(t, resumed, engine.procs.rows[0].attempt)
}

func TestAttempt_TheGiveBackOfTheEarlierAttemptKeepsTheAgentGroupsOfTheResumedOne(t *testing.T) {
	t.Parallel()

	engine := newProcEngine(t, nil)
	engine.deps.Lifecycle = &fakeLifecycle{failing: map[string]bool{}}
	engine.dir = t.TempDir()

	in := ItemInput{Item: testItem, Project: testProject}
	earlier, resumed := &attempt{id: testRun, in: in}, &attempt{id: testRun, in: in}

	mine, theirs := startSleeper(t), startSleeper(t)
	require.NoError(t, engine.procs.add(t.Context(), earlier, mine))
	require.NoError(t, engine.procs.add(t.Context(), resumed, theirs))

	engine.giveBack(t.Context(), earlier)

	require.Len(t, engine.procs.rows, 1, "the give-back of the earlier attempt forgot the agent groups of the resumed attempt")
	assert.Same(t, resumed, engine.procs.rows[0].attempt)
}

func TestAttempt_AGoodRenewalOfTheEarlierAttemptLeavesTheTrustOfTheResumedClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)

	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	var first runOutcome

	resumed, next := resumeInsideBeat(t, harness, engine, client, done, &first)

	harness.tick(t)
	require.Error(t, first.err, "the first attempt ended at its stop")

	require.False(t, resumed.renewed.IsZero(), "the resumed claim was trusted inside the renewal")
	require.Equal(t, resumed.renewed, receive(t, next).renewed, "the good renewal of the earlier attempt stamped the claim of the resumed run")
}

func TestAttempt_AFailedRenewalOfTheEarlierAttemptLeavesTheResumedClaimWithoutAFailure(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)

	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	// Only the token of the first take fails; the resumed run renews under its fresh token.
	harness.beats.fail(testOwner+testItem, "", errFake)
	var first runOutcome

	resumed, next := resumeInsideBeat(t, harness, engine, client, done, &first)

	harness.tick(t)
	require.Error(t, first.err, "the first attempt ended at its stop")

	require.Empty(t, resumed.failure, "the resumed claim held no failure inside the renewal")
	require.Empty(t, receive(t, next).failure, "the failed renewal of the earlier attempt wrote its failure into the claim of the resumed run")
}

// resumingLifecycle runs resume once, right after the first release frees the claim: beehest resume of another process takes the item in that window.
type resumingLifecycle struct {
	lifecycle

	resume func()
	calls  atomic.Int32
}

func (r *resumingLifecycle) Release(ctx context.Context, project, item, owner string) error {
	err := r.lifecycle.Release(ctx, project, item, owner)
	if r.calls.Add(1) == 1 {
		r.resume()
	}

	return err
}

func TestAttempt_TheGiveBackOfTheEarlierAttemptKeepsTheTokenThatResumeSavedForTheResumedRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)

	client := harness.client(t, testVersion)

	var (
		done  <-chan runOutcome
		first runOutcome
	)

	resumed := make(chan error, 1)

	// The resume waits for the outcome of the first attempt: the run reads the store row to report it, and a resume that lands first opens the row again.
	engine := harness.engineWith(t, func(e *Engine) {
		e.deps.Lifecycle = &resumingLifecycle{lifecycle: e.deps.Lifecycle, resume: func() {
			first = <-done
			resumed <- client.Resume(testItem)
		}}
	})

	done = runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	require.NoError(t, client.Cancel(testItem))
	harness.tick(t)
	require.NoError(t, receive(t, resumed), "beehest resume took the item once the give-back freed it")
	require.Error(t, first.err, "the first attempt ended at its cancel")

	// The give-back ends with the removal of the cancel marker.
	soon(t, func() bool { return !fileExists(cancelPath(engine.dir, testRun)) }, "the give-back of the earlier attempt ends")

	saved, err := os.ReadFile(ownerPath(engine.dir, testRun))
	require.NoError(t, err, "the give-back of the earlier attempt removed the token that beehest resume saved for the resumed run")
	require.Equal(t, testOwner+testItem+"#2", string(saved))
}

func TestAttempt_TheGiveBackOfACancelMarkerKeepsTheTokenAndTheMarkerOfTheResumedRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)

	in := ItemInput{Item: testItem, Project: testProject}
	require.NoError(t, saveOwner(engine.dir, testRun, "owner-1"))
	require.NoError(t, markCancel(engine.dir, testRun, cancelMark{ItemInput: in, Owner: "owner-1"}))

	// The sweep and the stray give-back make their attempt from the marker they read, and give it back. Inside the release, beehest resume saves a fresh token
	// and a cancel of the resumed run writes its marker.
	pending, err := pendingCancels(engine.dir, engine.logger)
	require.NoError(t, err)

	engine.deps.Lifecycle = &resumingLifecycle{lifecycle: engine.deps.Lifecycle, resume: func() {
		require.NoError(t, saveOwner(engine.dir, testRun, "owner-2"))
		require.NoError(t, markCancel(engine.dir, testRun, cancelMark{ItemInput: in, Owner: "owner-2"}))
	}}

	engine.giveBack(t.Context(), pending[testRun].attempt(testRun))

	assert.Equal(t, []string{"owner-1"}, harness.life.releasedBy(), "the sweep releases the token of its marker")

	saved, err := savedOwner(engine.dir, testRun)
	require.NoError(t, err)
	assert.Equal(t, "owner-2", saved, "the sweep removed the token that beehest resume saved for the resumed run")

	mark, err := readCancel(engine.dir, testRun)
	require.NoError(t, err, "the sweep removed the cancel marker of the resumed run")
	assert.Equal(t, "owner-2", mark.Owner)
}
