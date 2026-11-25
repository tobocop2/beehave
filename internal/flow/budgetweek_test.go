package flow

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ledger"
)

// weekLedger opens an engine and writes one agent execution of the cost of its entry for each entry of costs, started at start, with no run in the store.
// The wall clock of the engine reads the value that the returned pointer holds, and the real time while it holds none.
func weekLedger(t *testing.T, costs map[string]float64, start time.Time) (*Engine, *atomic.Pointer[time.Time]) {
	t.Helper()

	rig := newAgentRig(t)

	var at atomic.Pointer[time.Time]

	rig.h.wall = func() time.Time {
		if set := at.Load(); set != nil {
			return *set
		}

		return time.Now()
	}

	engine := rig.h.opened(t)

	for item, cost := range costs {
		seedSpend(t, engine, "call-"+item, runID(item, 1), item, start, cost)
	}

	return engine, &at
}

func TestWeekCap_CostsOfAllItemsInsideTheWindowOverTheCapRefuseTheNextStep(t *testing.T) {
	t.Parallel()

	engine, _ := weekLedger(t, map[string]float64{"1": 3, "2": 3}, time.Now())
	engine.maxWeekBudget = 5

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

	engine, at := weekLedger(t, map[string]float64{"1": 3, "2": 3}, time.Now())
	engine.maxWeekBudget = 5

	later := time.Now().Add(8 * 24 * time.Hour)
	at.Store(&later)

	got, err := engine.checkCaps("q", runID("3", 1))

	require.NoError(t, err)
	assert.Nil(t, got.Stop, "the executions started eight days before the check")
}

func TestWeekCap_AnExecutionThatStartedExactlyOneWindowAgoCountsAndOneMillisecondOlderDoesNot(t *testing.T) {
	t.Parallel()

	started := time.Now().Truncate(time.Millisecond)
	engine, at := weekLedger(t, map[string]float64{"1": 6}, started)
	engine.maxWeekBudget = 5

	onTheEdge := started.Add(weekWindow)
	at.Store(&onTheEdge)

	got, err := engine.checkCaps("q", runID("2", 1))
	require.NoError(t, err)
	require.NotNil(t, got.Stop, "an execution that started at the start of the window counts")

	past := started.Add(weekWindow + time.Millisecond)
	at.Store(&past)

	got, err = engine.checkCaps("q", runID("2", 1))
	require.NoError(t, err)
	assert.Nil(t, got.Stop, "an execution that started before the start of the window does not count")
}

func TestWeekCap_AnAbsentCapStopsNothingWhateverTheLedgerHolds(t *testing.T) {
	t.Parallel()

	engine, _ := weekLedger(t, map[string]float64{"1": 600}, time.Now())

	got, err := engine.checkCaps("q", runID("2", 1))

	require.NoError(t, err)
	assert.Nil(t, got.Stop)
}

func TestWeekCap_AnExecutionWithNoCostFailsTheCheckAndNamesTheCap(t *testing.T) {
	t.Parallel()

	engine, _ := weekLedger(t, map[string]float64{"1": 1}, time.Now())
	engine.maxWeekBudget = 5
	seedSpend(t, engine, "call-open", runID("2", 1), "2", time.Now(), nil)

	got, err := engine.checkCaps("q", runID("3", 1))

	require.ErrorContains(t, err, "the cost is unknown")
	require.ErrorContains(t, err, "agents.max_week_budget_usd")
	assert.Nil(t, got.Stop)
}

func TestWeekCap_AStepOverTheCapWaitsForARaiseAndARestartFinishesTheRound(t *testing.T) {
	t.Parallel()

	capRestart(t, func(h *harness, limit float64) { h.opts.MaxWeekBudget = limit }, scopeWeek, 20)
}

func TestWeekCap_ACapRaisedToNoCapLetsTheHeldStepStart(t *testing.T) {
	t.Parallel()

	capRestart(t, func(h *harness, limit float64) { h.opts.MaxWeekBudget = limit }, scopeWeek, 0)
}

func TestWeekCap_ALedgerWithNoRowReadsNoSpend(t *testing.T) {
	t.Parallel()

	engine := newAgentRig(t).h.opened(t)

	spent, err := engine.weekSpend("any")

	require.NoError(t, err)
	assert.InDelta(t, 0.0, spent.Cost, 0)
	assert.False(t, spent.Unknown)
}

func TestWeekCap_ACapCheckWhoseLedgerReadFailsReturnsTheErrorAndNoStop(t *testing.T) {
	t.Parallel()

	engine, _ := weekLedger(t, map[string]float64{"1": 6}, time.Now())
	closed, err := ledger.Open(t.Context(), t.TempDir())
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	failing := &Engine{ctx: engine.ctx, ledger: closed, maxWeekBudget: 5}

	got, err := failing.checkCaps("q", runID("2", 1))

	require.ErrorContains(t, err, "sum the spend in the ledger")
	assert.Nil(t, got.Stop)
}
