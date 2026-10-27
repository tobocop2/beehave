package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
	"github.com/tobocop2/obee/internal/model"
)

// gateInAgents holds the first agent start until the test lets it go, after the phase checked the claim and before the agent runs.
// It signals returned when the held start ended.
type gateInAgents struct {
	inner    *fakeAgents
	entered  chan struct{}
	hold     chan struct{}
	returned chan struct{}
	once     sync.Once
}

func (g *gateInAgents) Run(ctx context.Context, request AgentRequest) (agent.Result, error) {
	g.once.Do(func() {
		g.entered <- struct{}{}
		<-g.hold
	})

	result, err := g.inner.Run(ctx, request)
	g.returned <- struct{}{}

	return result, err
}

func TestRunAgent_AClaimLostBeforeTheAgentStartsStartsNoAgent(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	gated := &gateInAgents{inner: harness.agents, entered: make(chan struct{}, 1), hold: make(chan struct{}), returned: make(chan struct{}, 1)}
	clk := &leaseClock{now: time.Unix(1_000_000, 0)}
	engine := launchedWith(t, harness, func(deps *Deps) {
		deps.Agents = gated
		clk.install(&deps.Clock)
	})
	done := runInBackground(engine, testItem)

	<-gated.entered

	clk.advance(trustLimit)
	engine.beatClaims()

	outcome := <-done
	require.ErrorIs(t, outcome.err, errClaimLost)

	close(gated.hold)
	<-gated.returned

	assert.Empty(t, harness.agents.seen(), "the claim was lost before the agent started, so the start found its context done")
}

func TestPhase_TheBriefIsKeyedByTheRunThatQueuedThePhase(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	briefs := harness.briefs.seen()
	require.NotEmpty(t, briefs)

	for _, request := range briefs {
		assert.Equal(t, runID(testItem, 1), request.Run)
	}
}

func TestPhase_NoBriefIsWrittenForARunThatHoldsNoClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := launchedWith(t, harness, func(deps *Deps) { deps.Clock.Grace = 20 * time.Millisecond })

	handle, err := dbos.RunWorkflow(engine.ctx, engine.phase, PhaseInput{Run: "item-12-r9", Item: testItem, Project: testProject, Step: model.StepPlan, Login: testLogin},
		dbos.WithQueue(engine.queue), dbos.WithQueuePartitionKey(testLogin))
	require.NoError(t, err)

	_, err = handle.GetResult()

	require.ErrorContains(t, err, errClaimLost.Error())
	assert.Empty(t, harness.briefs.seen(), "an orphan phase of a lost run writes no brief that a new run of the item could read")
	assert.Empty(t, harness.agents.seen())
}

func TestRunAgent_TheAgentRunsUnderTheContextOfTheClaim(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.agents.gate = make(chan struct{})
	harness.agents.pid = startSleeper(t)
	finish := sync.OnceFunc(func() { close(harness.agents.gate) })
	t.Cleanup(finish)

	engine := launchedWith(t, harness, func(*Deps) {})
	done := runInBackground(engine, testItem)

	eventually(t, func() bool { return harness.agents.running() == 1 })

	// A claim that ends, as the end of the run ends it, ends the context of the agent and kills its group.
	engine.holds.drop(runID(testItem, 1))
	requireDies(t, harness.agents.pid)
	finish()

	<-done
}

func TestLose_KillsNoAgentOfAnotherRunOfTheSameItem(t *testing.T) {
	t.Parallel()

	run := runTimed(t, nil)
	other := startSleeper(t)
	require.NoError(t, run.engine.procs.add(t.Context(), testItem+"/review/7", other))

	run.clock.advance(trustLimit)
	run.engine.beatClaims()

	requireDies(t, run.agent)
	time.Sleep(300 * time.Millisecond)
	assert.True(t, alive(other), "the agent of another run of the item must survive the loss of this run")
}
