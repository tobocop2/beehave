package flow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

// weekStore runs one item run for each entry of costs, with no run above any of them. Each run records the cost of its entry the way an agent call does.
// The wall clock of the engine reads the value that the returned pointer holds, and the real time while it holds none.
func weekStore(t *testing.T, rig *agentRig, costs map[string]float64) (*Engine, *atomic.Pointer[time.Time]) {
	t.Helper()

	var at atomic.Pointer[time.Time]

	rig.h.wall = func() time.Time {
		if set := at.Load(); set != nil {
			return *set
		}

		return time.Now()
	}

	engine := rig.h.opened(t)

	solo := func(ctx dbos.Context, in ItemInput) (string, error) {
		_, err := runStep(engine, ctx, "agent#1/spent0", func(context.Context) (stepResult, error) {
			return stepResult{Outputs: spendOutputs(agent.Spend{Cost: costs[in.Item]})}, nil
		})

		return in.Item, err
	}

	dbos.RegisterWorkflow(engine.ctx, solo, dbos.WithWorkflowName(itemName))
	require.NoError(t, dbos.Launch(engine.ctx))

	for item := range costs {
		handle, err := dbos.RunWorkflow(engine.ctx, solo, ItemInput{Item: item, Project: testProject}, dbos.WithWorkflowID(runID(item, 1)))
		require.NoError(t, err)

		_, err = handle.GetResult()
		require.NoError(t, err)
	}

	return engine, &at
}

// costTimeOf is the time that the window reads for the recorded cost step of the run, as the lines of the store hold it.
func costTimeOf(t *testing.T, engine *Engine, run string) time.Time {
	t.Helper()

	records, err := dbos.GetWorkflowSteps(engine.ctx, run, dbos.WithStepsLoadOutput(true))
	require.NoError(t, err)

	lines, err := runLines(viewOf(engine.ctx, engine.dir), run, records, "", false, time.Time{})
	require.NoError(t, err)
	require.Len(t, lines, 1)
	// The test step has no record that ends a call, so the line has its start and no end, and the window counts from the start.
	require.True(t, lines[0].Ended.IsZero())
	require.False(t, lines[0].Started.IsZero())

	return lines[0].Started
}

func TestWeekCap_CostsOfAllItemsInsideTheWindowOverTheCapRefuseTheNextStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	engine, at := weekStore(t, rig, map[string]float64{"1": 3, "2": 3})
	engine.maxWeekBudget = 5

	later := costTimeOf(t, engine, runID("2", 1)).Add(time.Hour)
	at.Store(&later)

	// The next step belongs to a third item, which has recorded nothing: only the sum of the other two items can stop it.
	got, err := engine.checkCaps("q", runID("3", 1))

	require.NoError(t, err)
	require.NotNil(t, got.Stop, "the sum of the two items is over the cap of the week")
	assert.Equal(t, scopeWeek, got.Stop.Scope)
	assert.InDelta(t, 5.0, got.Stop.Cap, 0)
	assert.InDelta(t, 6.0, got.Stop.Spent, 0)
	assert.Contains(t, got.Stop.Note, "the cap for the last seven days is $5 and the agent steps of all items spent $6")
	assert.Contains(t, got.Stop.Note, "agents.max_week_budget_usd")
	assert.Contains(t, got.Stop.Note, "BEEHAVE_AGENTS_MAX_WEEK_BUDGET_USD")
	assert.Equal(t, "stopped at the week cap $5, spent $6", got.Stop.text())
}

func TestWeekCap_TheSameCostsOutsideTheWindowAllowTheNextStep(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	engine, at := weekStore(t, rig, map[string]float64{"1": 3, "2": 3})
	engine.maxWeekBudget = 5

	later := time.Now().Add(8 * 24 * time.Hour)
	at.Store(&later)

	got, err := engine.checkCaps("q", runID("3", 1))

	require.NoError(t, err)
	assert.Nil(t, got.Stop, "the costs ended eight days before the check")
}

func TestWeekCap_ACostThatEndedExactlyOneWindowAgoCountsAndOneNanosecondOlderDoesNot(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	engine, at := weekStore(t, rig, map[string]float64{"1": 6})
	engine.maxWeekBudget = 5

	ended := costTimeOf(t, engine, runID("1", 1))

	onTheEdge := ended.Add(weekWindow)
	at.Store(&onTheEdge)

	got, err := engine.checkCaps("q", runID("2", 1))
	require.NoError(t, err)
	require.NotNil(t, got.Stop, "a cost that ended at the start of the window counts")

	past := ended.Add(weekWindow + time.Nanosecond)
	at.Store(&past)

	got, err = engine.checkCaps("q", runID("2", 1))
	require.NoError(t, err)
	assert.Nil(t, got.Stop, "a cost that ended before the start of the window does not count")
}

func TestWeekCap_AnAbsentCapStopsNothingWhateverTheStoreHolds(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	engine, _ := weekStore(t, rig, map[string]float64{"1": 600})

	got, err := engine.checkCaps("q", runID("2", 1))

	require.NoError(t, err)
	assert.Nil(t, got.Stop)
}

func TestWeekCap_ACostWithNoTimeCountsInTheWindow(t *testing.T) {
	t.Parallel()

	// The cost of a failed run whose call has no line yet gets a line of its own, which holds no time.
	lines := withFailedRuns(nil, []failedRun{{Call: "x", Spend: agent.Spend{Cost: 3}}})

	require.Len(t, lines, 1)
	require.True(t, lines[0].Started.IsZero() && lines[0].Ended.IsZero())
	assert.True(t, inWindow(lines[0], time.Now()), "a cost that cannot be placed in time counts, so the cap errs toward stopping")
}

func TestWeekCap_AStepOverTheCapWaitsForARaiseAndARestartFinishesTheRound(t *testing.T) {
	t.Parallel()

	capRestart(t, func(h *harness, limit float64) { h.opts.MaxWeekBudget = limit }, scopeWeek, 20)
}

func TestWeekCap_ACapRaisedToNoCapLetsTheHeldStepStart(t *testing.T) {
	t.Parallel()

	capRestart(t, func(h *harness, limit float64) { h.opts.MaxWeekBudget = limit }, scopeWeek, 0)
}

func TestWeekCap_ARunThatListsNoItemRunsReadsNoSpend(t *testing.T) {
	t.Parallel()

	engine := newAgentRig(t).h.opened(t)

	spent, err := engine.weekSpend("any")

	require.NoError(t, err)
	assert.InDelta(t, 0.0, spent.Cost, 0)
}

func TestWeekCap_ACapCheckWhoseReadOfARunFailsNamesTheRunAndReturnsNoStop(t *testing.T) {
	t.Parallel()

	engine, _ := weekStore(t, newAgentRig(t), map[string]float64{"1": 6})
	// The first read lists the item runs, and the second reads the steps of the only run.
	failing := &Engine{ctx: &failingRead{Context: engine.ctx, at: 2}, maxWeekBudget: 5}

	got, err := failing.checkCaps("q", runID("2", 1))

	require.ErrorContains(t, err, "read the steps of "+runID("1", 1))
	assert.Nil(t, got.Stop)
}

func TestWeekCap_ACapCheckWhoseListOfRunsFailsReturnsTheErrorAndNoStop(t *testing.T) {
	t.Parallel()

	engine, _ := weekStore(t, newAgentRig(t), map[string]float64{"1": 6})
	failing := &Engine{ctx: &failingRead{Context: engine.ctx, at: 1}, maxWeekBudget: 5}

	got, err := failing.checkCaps("q", runID("2", 1))

	require.ErrorContains(t, err, "the store failed")
	assert.Nil(t, got.Stop)
}
