package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sleep moves the wall reading of the clock and leaves the monotonic reading behind, as a sleeping machine does.
func (c *leaseClock) sleep(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.asleep += by
}

// timerSet is the After of a test clock: it records each timer and fires them on demand.
type timerSet struct {
	mu     sync.Mutex
	timers []*testTimer
}

type testTimer struct {
	after   time.Duration
	fire    func()
	stopped bool
}

func (s *timerSet) after(d time.Duration, f func()) func() {
	s.mu.Lock()
	defer s.mu.Unlock()

	timer := &testTimer{after: d, fire: f}
	s.timers = append(s.timers, timer)

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		timer.stopped = true
	}
}

// all lists the timers set so far.
func (s *timerSet) all() []*testTimer {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]*testTimer(nil), s.timers...)
}

// stoppedAt reports whether the timer at index was stopped.
func (s *timerSet) stoppedAt(index int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.timers[index].stopped
}

// fireLast starts the function of the newest timer, as the clock does when its time is up.
func (s *timerSet) fireLast() {
	all := s.all()
	all[len(all)-1].fire()
}

// timed is a run whose agent step is in flight, over a clock the test sets and timers the test fires.
type timed struct {
	inFlight
	timers *timerSet
}

func runTimed(t *testing.T, change func(*Deps)) timed {
	t.Helper()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	harness.agents.pid = startSleeper(t)
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	timers := &timerSet{}
	finish := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(finish)

	engine := launchedWith(t, harness, func(deps *Deps) {
		clk.install(&deps.Clock)
		deps.Clock.After = timers.after

		if change != nil {
			change(deps)
		}
	})
	done := runInBackground(engine, testItem)

	eventually(t, func() bool { return harness.agents.running() == 1 })

	return timed{inFlight: inFlight{harness: harness, engine: engine, clock: clk, done: done, agent: harness.agents.pid, finish: finish}, timers: timers}
}

// gatedBeats holds each renewal until the test sends on release or the context of the renewal ends, then fails with errFake.
// It reports the deadline of the context of each renewal.
type gatedBeats struct {
	deadlines chan time.Time
	release   chan struct{}
}

func (g gatedBeats) Beat(ctx context.Context, _, _ string) (string, error) {
	deadline, _ := ctx.Deadline()
	g.deadlines <- deadline

	select {
	case <-g.release:
	case <-ctx.Done():
	}

	return "", errFake
}

func TestTrust_ATakeThisRunMadeIsTrustedFromTheStartOfTheTake(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	harness.life.onClaim = func() { clk.advance(10 * time.Second) }

	timers := &timerSet{}
	engine := launchedWith(t, harness, func(deps *Deps) {
		clk.install(&deps.Clock)
		deps.Clock.After = timers.after
	})

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	first := timers.all()[0]
	assert.Equal(t, trustLimit-10*time.Second, first.after, "the lease began when the take began, 10 seconds before the claim step returned")
	assert.True(t, timers.stoppedAt(0), "the end of the run stops the timer")
	assert.Empty(t, harness.beats.seen(), "a take this run made needs no renewal before the first agent")
}

func TestTrust_AGoodRenewalArmsANewTimerAndStopsTheOldOne(t *testing.T) {
	t.Parallel()

	run := runTimed(t, nil)

	run.clock.advance(100 * time.Second)
	run.engine.beatClaims()

	timers := run.timers.all()
	require.Len(t, timers, 2)
	assert.True(t, run.timers.stoppedAt(0), "the timer of the take is stopped")
	assert.False(t, run.timers.stoppedAt(1))
	assert.Equal(t, trustLimit, timers[1].after, "the new timer counts from the start of the renewal")

	timers[0].fire()
	assert.True(t, alive(run.agent), "a timer that a renewal replaced ends nothing")
	assert.NotEmpty(t, run.engine.holds.snapshot())

	run.finish()
	require.NoError(t, (<-run.done).err)
}

func TestTrust_ARenewalOfARunThatIsGoneLeavesNoTimerBehind(t *testing.T) {
	t.Parallel()

	timers := &timerSet{}
	harness := newHarness(t)
	engine := launchedWith(t, harness, func(deps *Deps) { deps.Clock.After = timers.after })

	engine.trust("run-gone", time.Now())

	require.Len(t, timers.all(), 1)
	assert.True(t, timers.stoppedAt(0))
}

func TestTrust_ARenewalIsStampedAtItsStartNotAtItsEnd(t *testing.T) {
	t.Parallel()

	run := runTimed(t, nil)

	run.clock.advance(50 * time.Second)

	start := run.clock.Now()
	once := sync.Once{}

	run.harness.beats.hook(func() { once.Do(func() { run.clock.advance(100 * time.Second) }) })
	run.engine.beatClaims()

	for _, row := range run.engine.holds.snapshot() {
		assert.True(t, row.renewed.Equal(start), "the renewal took 100 seconds, and the lease began when it started")
	}

	run.finish()
	require.NoError(t, (<-run.done).err)
}

func TestTrust_AHangingRenewalCannotHoldTheCancelPastTheDeadline(t *testing.T) {
	t.Parallel()

	beats := gatedBeats{deadlines: make(chan time.Time, 4), release: make(chan struct{})}
	run := runTimed(t, func(deps *Deps) {
		deps.Beats = beats
		deps.Clock.Beat = 30 * time.Second
	})

	t.Cleanup(func() { close(beats.release) })

	run.clock.advance(trustLimit - time.Second)

	go run.engine.beatClaims()

	deadline := <-beats.deadlines
	assert.LessOrEqual(t, time.Until(deadline), time.Second, "the renewal gets the time left to the deadline, not its 30 second limit")

	run.clock.advance(time.Second)
	run.timers.fireLast()

	requireDies(t, run.agent)

	outcome := <-run.done
	require.ErrorIs(t, outcome.err, errClaimLost)
	require.ErrorContains(t, outcome.err, "its lease can end")
}

func TestTrust_ASleepOfTheMachineEndsTheTrustAtTheNextTick(t *testing.T) {
	t.Parallel()

	run := runTimed(t, nil)

	run.clock.sleep(trustLimit)
	run.engine.beatClaims()

	requireDies(t, run.agent)

	outcome := <-run.done
	require.ErrorIs(t, outcome.err, errClaimLost)
	assert.Empty(t, run.harness.beats.seen(), "the claim is lost at once; no renewal runs")
}

func TestTrust_TheReasonOfALostDeadlineNamesTheLastFailure(t *testing.T) {
	t.Parallel()

	run := runTimed(t, nil)

	run.harness.beats.fail(testOwner+testItem, "", errFake)
	run.clock.advance(time.Minute)
	run.engine.beatClaims()
	run.timers.fireLast()

	requireDies(t, run.agent)

	outcome := <-run.done
	require.ErrorIs(t, outcome.err, errClaimLost)
	require.ErrorContains(t, outcome.err, "the last renewal failed with: "+errFake.Error())
}

// failedAtTheAgent runs an item to a failed agent, then lets the agent work again. It returns the harness, the engine, the clock of the engine
// and the number of agent runs so far.
func failedAtTheAgent(t *testing.T) (*harness, *Engine, *leaseClock, int) {
	t.Helper()

	harness := newHarness(t)
	harness.agents.err = errFake
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	engine := launchedWith(t, harness, func(deps *Deps) { clk.install(&deps.Clock) })

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, errFake.Error())
	require.Empty(t, harness.beats.seen())

	harness.agents.mu.Lock()
	agents := len(harness.agents.requests)
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	return harness, engine, clk, agents
}

// resumeTheRun puts the failed run back, as obee resume does. The claim step of the run then replays with the fresh claim that the resume took,
// so the run does not know how old that lease is.
func resumeTheRun(t *testing.T, harness *harness) {
	t.Helper()

	require.NoError(t, harness.client(t, testVersion).Resume(testItem))
}

func TestFirstTrust_ARunWithAReplayedClaimNeedsOneGoodRenewalBeforeItsWork(t *testing.T) {
	t.Parallel()

	harness, engine, _, _ := failedAtTheAgent(t)
	resumeTheRun(t, harness)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	assert.Equal(t, []string{testProject + "/" + testOwner + testItem + "#2"}, harness.beats.seen(), "one renewal of the fresh claim ran before the work continued")
}

func TestFirstTrust_ARenewalThatFailsOnceIsTriedAgainBeforeTheWorkStarts(t *testing.T) {
	t.Parallel()

	harness, engine, _, _ := failedAtTheAgent(t)
	owner := testOwner + testItem + "#2"

	// The renewal fails once: the hook runs after the fake read that it fails, and it mends the tracker for the next renewal.
	harness.beats.fail(owner, "", errFake)
	harness.beats.hook(func() { harness.beats.fail("", "", nil) })
	resumeTheRun(t, harness)

	result, err := engine.Run(testItem, testProject)
	require.NoError(t, err, "a tracker call that was busy once does not end the resumed run")
	assert.Equal(t, ItemResult{Item: testItem}, result)
	assert.Equal(t, []string{testProject + "/" + owner, testProject + "/" + owner}, harness.beats.seen(), "the first renewal failed and the second worked")
}

func TestFirstTrust_ARunThatIsCanceledWhileItTriesTheRenewalAgainTriesNoMore(t *testing.T) {
	t.Parallel()

	harness, engine, _, agents := failedAtTheAgent(t)
	harness.beats.fail(testOwner+testItem+"#2", "", errFake)
	harness.beats.hook(func() { engine.cancels.stop(runID(testItem, 1)) })
	resumeTheRun(t, harness)

	_, err := engine.Run(testItem, testProject)
	require.Error(t, err)
	assert.Len(t, harness.beats.seen(), 1, "the cancel ended the wait before the second try")
	assert.Len(t, harness.agents.seen(), agents)
}

func TestFirstTrust_ARenewalThatFailsUntilTheLimitKeepsTheWorkFromStarting(t *testing.T) {
	t.Parallel()

	harness, engine, clk, agents := failedAtTheAgent(t)
	harness.beats.fail(testOwner+testItem+"#2", "", errFake)
	harness.beats.hook(func() { clk.advance(2 * time.Minute) })
	resumeTheRun(t, harness)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, "before the first agent starts")
	require.ErrorContains(t, err, errFake.Error())
	assert.Greater(t, len(harness.beats.seen()), 1, "the renewal was tried again until the limit passed")

	assert.Len(t, harness.agents.seen(), agents, "no step ran on a claim nobody renewed")
}

func TestFirstTrust_ATrackerThatSaysTheClaimIsGoneEndsTheRunBeforeItsWork(t *testing.T) {
	t.Parallel()

	harness, engine, _, agents := failedAtTheAgent(t)
	harness.beats.fail(testOwner+testItem+"#2", "heartbeat for session-x failed: it holds no claim row", nil)
	resumeTheRun(t, harness)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, errClaimLost.Error())
	require.ErrorContains(t, err, "it holds no claim row")
	assert.Len(t, harness.agents.seen(), agents)
}

// phaseCount counts the phase workflows in the store that have one of the statuses, or all of them when no status is named.
func phaseCount(t *testing.T, engine *Engine, statuses ...dbos.WorkflowStatusType) int {
	t.Helper()

	options := []dbos.ListWorkflowsOption{dbos.WithFilterName(phaseName)}
	if len(statuses) > 0 {
		options = append(options, dbos.WithFilterStatus(statuses...))
	}

	rows, err := dbos.ListWorkflows(engine.ctx, options...)
	require.NoError(t, err)

	return len(rows)
}

func TestRunAgent_NoAgentStartsAfterTheClaimIsLostWhileThePhaseWaitsToStart(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	gated := &gateInAgents{inner: harness.agents, entered: make(chan struct{}, 1), hold: make(chan struct{}), returned: make(chan struct{}, 1)}
	engine := launchedWith(t, harness, func(deps *Deps) { deps.Agents = gated })
	done := runInBackground(engine, testItem)

	<-gated.entered

	harness.beats.fail(testOwner+testItem, "heartbeat for session-x failed: it holds no claim row", nil)
	harness.tick(t)

	outcome := <-done
	require.ErrorIs(t, outcome.err, errClaimLost)

	close(gated.hold)
	<-gated.returned
	eventually(t, func() bool { return phaseCount(t, engine, dbos.WorkflowStatusError) == 1 })

	assert.Empty(t, harness.agents.seen(), "the phase read the lost claim and started no agent")
}

func TestQueuePhase_QueuesNoPhaseForARunThatHoldsNoTrustedClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	_, err := engine.queuePhase(engine.ctx, PhaseInput{Item: testItem, Project: testProject, Login: testLogin})

	require.ErrorIs(t, err, errClaimLost)
	assert.Zero(t, phaseCount(t, engine), "the guard on the phase start queued nothing")
	assert.Empty(t, harness.agents.seen())
}

func TestRunAgent_AnOrphanPhaseOfALostRunStartsNoAgentWhenANewRunHoldsTheItem(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	gated := &gateInAgents{inner: harness.agents, entered: make(chan struct{}, 1), hold: make(chan struct{}), returned: make(chan struct{}, 1)}
	engine := launchedWith(t, harness, func(deps *Deps) {
		deps.Agents = gated
		deps.Clock.Grace = 20 * time.Millisecond
	})
	done := runInBackground(engine, testItem)

	<-gated.entered

	harness.beats.fail(testOwner+testItem, "heartbeat for session-x failed: it holds no claim row", nil)
	harness.tick(t)

	outcome := <-done
	require.ErrorIs(t, outcome.err, errClaimLost)

	// A new run takes the item and holds a trusted claim on it. The orphan phase belongs to the lost run, not to this one.
	engine.holds.add("item-"+testItem+"-r2", claimHold{project: testProject, owner: testOwner + testItem + "#2", renewed: time.Now()})

	close(gated.hold)
	<-gated.returned
	eventually(t, func() bool { return phaseCount(t, engine, dbos.WorkflowStatusError) == 1 })

	assert.Empty(t, harness.agents.seen(), "the claim of the new run is not the claim of the orphan")
}

func TestRunAgent_APhaseReopenedByResumeWaitsForTheFirstTrustOfItsRunBeforeItStartsAnAgent(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.err = errFake
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.ErrorContains(t, err, errFake.Error())

	started := len(harness.agents.seen())

	harness.agents.mu.Lock()
	harness.agents.err = nil
	harness.agents.mu.Unlock()

	// The renewal of the fresh claim hangs, so the run has no first trust while the reopened phase is in the queue.
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	harness.beats.hook(func() { <-release })

	require.NoError(t, harness.client(t, testVersion).Resume(testItem))

	done := runInBackground(engine, testItem)

	// A worker took the reopened phase from the queue: its status is pending.
	eventually(t, func() bool { return phaseCount(t, engine, dbos.WorkflowStatusPending) == 1 })
	assert.Len(t, harness.agents.seen(), started, "the reopened phase is in a worker and starts no agent while its run has no trusted claim")

	unblock()

	outcome := <-done
	require.NoError(t, outcome.err)
	assert.Equal(t, ItemResult{Item: testItem}, outcome.result)
	assert.Greater(t, len(harness.agents.seen()), started, "the phase started its agent once the run held a trusted claim")
}

func TestAwaitClaim_AnOpenRunWithNoFirstTrustIsWaitedForUntilTheContextEnds(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	run := runID(testItem, 1)

	open, err := engine.runOpen(run)
	require.NoError(t, err)
	assert.False(t, open, "a run that is not in the store is not open")

	enqueueRun(t, harness.client(t, testVersion), run, ItemInput{Item: testItem, Project: testProject}, testVersion)

	open, err = engine.runOpen(run)
	require.NoError(t, err)
	assert.True(t, open)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = engine.awaitClaim(ctx, PhaseInput{Run: run, Item: testItem})

	require.ErrorIs(t, err, context.Canceled)
}

func TestAwaitClaim_AnOpenRunIsWaitedForPastTheGraceTime(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	engine.deps.Clock.Grace = 20 * time.Millisecond
	engine.deps.Clock.Poll = storePoll

	run := runID(testItem, 1)
	enqueueRun(t, harness.client(t, testVersion), run, ItemInput{Item: testItem, Project: testProject}, testVersion)

	go func() {
		time.Sleep(20 * engine.deps.Clock.Grace)
		engine.holds.add(run, claimHold{renewed: engine.deps.Clock.now()})
	}()

	claim, err := engine.awaitClaim(t.Context(), PhaseInput{Run: run, Item: testItem})

	require.NoError(t, err, "the run is open, so the grace time does not end the wait")
	require.NoError(t, claim.Err())
}

func TestAwaitClaim_TheGraceTimeCountsOnTheClockOfTheEngine(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	clk.install(&engine.deps.Clock)
	engine.deps.Clock.Poll = storePoll
	engine.deps.Clock.Grace = time.Minute

	done := make(chan error, 1)

	go func() {
		_, err := engine.awaitClaim(t.Context(), PhaseInput{Run: "item-12-r9", Item: testItem})
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("the wait ended before the grace time passed on the clock of the engine: %v", err)
	case <-time.After(10 * storePoll):
	}

	clk.advance(time.Minute)

	require.ErrorIs(t, <-done, errClaimLost)
}

func TestAwaitClaim_AGraceTimeLetsARunThatIsNotOpenYetTakeItsClaim(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	engine.deps.Clock.Grace = time.Minute
	engine.deps.Clock.Poll = storePoll

	go func() {
		time.Sleep(50 * time.Millisecond)
		engine.holds.add("run-late", claimHold{renewed: time.Now()})
	}()

	claim, err := engine.awaitClaim(t.Context(), PhaseInput{Run: "run-late", Item: testItem})

	require.NoError(t, err)
	require.NoError(t, claim.Err())
}

func TestAwaitClaim_ARunThatEndedWithNoTrustStopsTheWaitWhenTheGraceTimeIsOver(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	engine.deps.Clock.Grace = 20 * time.Millisecond
	engine.deps.Clock.Poll = storePoll

	_, err := engine.awaitClaim(t.Context(), PhaseInput{Run: "item-12-r9", Item: testItem})

	require.ErrorIs(t, err, errClaimLost)
	require.ErrorContains(t, err, "holds no claim on the item")
}
