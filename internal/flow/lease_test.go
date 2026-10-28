package flow

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leaseClock is the clock of the process, set by the test. Its monotonic reading is now. Its wall reading is now plus the time the machine slept.
type leaseClock struct {
	mu     sync.Mutex
	now    time.Time
	asleep time.Duration
}

func (c *leaseClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// Wall reads the wall clock: the monotonic reading and the time of the sleeps.
func (c *leaseClock) Wall() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now.Round(0).Add(c.asleep)
}

// install makes the clock of an engine read this clock.
func (c *leaseClock) install(into *clock) {
	into.Now, into.Wall = c.Now, c.Wall
}

func (c *leaseClock) advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(by)
}

// inFlight is a run whose agent step is in flight. The agent is a real process that the engine records, so the test sees the kill.
type inFlight struct {
	harness *harness
	engine  *Engine
	clock   *leaseClock
	done    <-chan runOutcome
	agent   int
	finish  func()
}

// runWithAgentInFlight starts the item with an agent that stays in flight until the test calls finish.
func runWithAgentInFlight(t *testing.T) inFlight {
	t.Helper()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	harness.agents.pid = startSleeper(t)
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	finish := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(finish)

	engine := launchedWith(t, harness, func(deps *Deps) { clk.install(&deps.Clock) })
	done := runInBackground(engine, testItem)

	eventually(t, func() bool { return harness.agents.running() == 1 })

	return inFlight{harness: harness, engine: engine, clock: clk, done: done, agent: harness.agents.pid, finish: finish}
}

func TestBeat_AFailedRenewalKeepsTheRunWhileTheLeaseCanStillBeLive(t *testing.T) {
	t.Parallel()

	run := runWithAgentInFlight(t)

	run.harness.beats.fail(testOwner+testItem, "", errFake)
	run.clock.advance(100 * time.Second)
	run.harness.tick(t)

	assert.NotEmpty(t, run.engine.holds.snapshot(), "one failed renewal before the end of the lease marks nothing")
	assert.True(t, alive(run.agent))

	run.harness.beats.fail(testOwner+testItem, "", nil)
	run.clock.advance(100 * time.Second)
	run.harness.tick(t)
	run.harness.beats.fail(testOwner+testItem, "", errFake)
	run.clock.advance(trustLimit - time.Second)
	run.harness.tick(t)

	assert.NotEmpty(t, run.engine.holds.snapshot(), "a renewal that worked moved the end of the trust forward")
	assert.True(t, alive(run.agent))

	run.finish()
	require.NoError(t, (<-run.done).err)
}

func TestBeat_RenewalsThatFailUntilTheLeaseEndsCancelTheStepWithinOneTick(t *testing.T) {
	t.Parallel()

	run := runWithAgentInFlight(t)

	run.harness.beats.fail(testOwner+testItem, "", errFake)
	run.clock.advance(trustLimit - time.Second)
	run.harness.tick(t)
	assert.NotEmpty(t, run.engine.holds.snapshot(), "one tick before the deadline the claim is still trusted")

	run.clock.advance(time.Second)
	run.harness.tick(t)

	assert.Empty(t, run.engine.holds.snapshot())
	requireDies(t, run.agent)

	outcome := <-run.done
	require.ErrorIs(t, outcome.err, errClaimLost)
	require.ErrorContains(t, outcome.err, errFake.Error())
	assert.Zero(t, countOf(run.harness.life.seen(), "release"), "the claim is not ours to release")
}

func TestBeat_ATrackerThatNamesAnotherHolderCancelsTheStepAtOnce(t *testing.T) {
	t.Parallel()

	run := runWithAgentInFlight(t)

	run.harness.beats.fail(testOwner+testItem, "heartbeat for session-x failed: it holds no claim row", nil)
	run.harness.tick(t)

	requireDies(t, run.agent)

	outcome := <-run.done
	require.ErrorIs(t, outcome.err, errClaimLost)
	require.ErrorContains(t, outcome.err, "it holds no claim row")
}
