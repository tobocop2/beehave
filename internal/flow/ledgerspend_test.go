package flow

import (
	"bytes"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ledger"
)

// seedSpend inserts one spend row into the ledger of the engine, as an agent step of an earlier process wrote it. A nil cost is an unknown cost.
func seedSpend(t *testing.T, engine *Engine, call, tree, item string, at time.Time, cost any) {
	t.Helper()

	_, err := engine.ledger.DB().ExecContext(t.Context(),
		`INSERT INTO spend (call, tree, run_id, step, session, item, pull_request, call_name, cost, at) VALUES (?, ?, ?, 1, 's', ?, 0, 'agent:fix#1', ?, ?)`,
		call, tree, tree, item, cost, at.UnixMilli())
	require.NoError(t, err)
}

// spendRow is one row of the spend table, as a test reads it.
type spendRow struct {
	Call, Tree, Run, Item, Name, Session string
	Cost                                 sql.NullFloat64
}

// spendRows lists the spend rows of the engine in the order that they started.
func spendRows(t *testing.T, engine *Engine) []spendRow {
	t.Helper()

	rows, err := engine.ledger.DB().QueryContext(t.Context(), `SELECT call, tree, run_id, item, call_name, session, cost FROM spend ORDER BY at, rowid`)
	require.NoError(t, err)

	defer func() { require.NoError(t, rows.Close()) }()

	var got []spendRow

	for rows.Next() {
		var row spendRow

		require.NoError(t, rows.Scan(&row.Call, &row.Tree, &row.Run, &row.Item, &row.Name, &row.Session, &row.Cost))

		got = append(got, row)
	}

	require.NoError(t, rows.Err())

	return got
}

func TestSpend_TheBudgetReadsTheLedgerNotTheRuns(t *testing.T) {
	t.Parallel()

	t.Run("the_tree_cap_sums_the_ledger_rows_of_the_tree", func(t *testing.T) {
		t.Parallel()

		engine := newAgentRig(t).h.opened(t)
		engine.maxTreeBudget = 5
		seedSpend(t, engine, "c1", runID("1", 1), "1", time.Now(), 3.0)
		seedSpend(t, engine, "c2", runID("1", 1), "1", time.Now(), 3.0)
		seedSpend(t, engine, "c3", runID("9", 1), "9", time.Now(), 100.0)

		got, err := engine.checkCaps("q", runID("1", 1))

		require.NoError(t, err)
		require.NotNil(t, got.Stop, "the two rows of the tree are over the cap, and no run of the store holds them")
		assert.Equal(t, scopeTree, got.Stop.Scope)
		assert.InDelta(t, 6.0, got.Stop.Spent, 0, "the row of another tree is not in the sum")
	})

	t.Run("the_week_cap_sums_the_ledger_rows_of_every_item_inside_the_window", func(t *testing.T) {
		t.Parallel()

		engine := newAgentRig(t).h.opened(t)
		engine.maxWeekBudget = 5
		seedSpend(t, engine, "c1", runID("1", 1), "1", time.Now(), 3.0)
		seedSpend(t, engine, "c2", runID("2", 1), "2", time.Now(), 3.0)
		seedSpend(t, engine, "c3", runID("3", 1), "3", time.Now().Add(-8*24*time.Hour), 100.0)

		got, err := engine.checkCaps("q", runID("4", 1))

		require.NoError(t, err)
		require.NotNil(t, got.Stop, "the two rows of the week are over the cap, and no run of the store holds them")
		assert.Equal(t, scopeWeek, got.Stop.Scope)
		assert.InDelta(t, 6.0, got.Stop.Spent, 0, "the row of eight days ago is not in the sum")
	})
}

func TestTreeCap_AnExecutionWithNoCostFailsTheCheckAndNamesTheCap(t *testing.T) {
	t.Parallel()

	engine := newAgentRig(t).h.opened(t)
	engine.maxTreeBudget = 5
	seedSpend(t, engine, "c1", runID("1", 1), "1", time.Now(), 1.0)
	seedSpend(t, engine, "c2", runID("1", 1), "1", time.Now(), nil)

	got, err := engine.checkCaps("q", runID("1", 1))

	require.ErrorContains(t, err, "the cost is unknown")
	require.ErrorContains(t, err, "agents.max_tree_budget_usd")
	assert.Nil(t, got.Stop)
}

func TestTreeCap_ACapCheckWhoseLedgerReadFailsReturnsTheErrorAndNoStop(t *testing.T) {
	t.Parallel()

	engine := newAgentRig(t).h.opened(t)
	closed, err := ledger.Open(t.Context(), t.TempDir())
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	failing := &Engine{ctx: engine.ctx, ledger: closed, maxTreeBudget: 5}

	got, err := failing.checkCaps("q", runID("1", 1))

	require.ErrorContains(t, err, "sum the spend in the ledger")
	assert.Nil(t, got.Stop)
}

func TestRetention_TheFixLimitHoldsAfterEveryRunOlderThanTheRetentionIsDeleted(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, "def main(item):\n    agent(name = \"fix\", prompt = \"prompts/work.md\", model = \"m\")\n")
	finishedRun(t, harness)

	harness.opts.Retention = time.Nanosecond
	later := harness.deps()
	later.Clock.Wall = func() time.Time { return time.Now().Add(time.Hour) }

	engine, err := NewEngine(t.Context(), harness.opts, later)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	rows, err := harness.client(t, testVersion).Status()
	require.NoError(t, err)
	require.Empty(t, rows, "the retention deleted the only run of the item")

	count, err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).countEarlier(t.Context(), "fix")

	require.NoError(t, err)
	assert.Equal(t, 1, count, "the fix call of the deleted run still counts")
}

func TestSpend_AfterTheRetentionTheSameRunIdWritesTheRowOfTheNewRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, "def main(item):\n    agent(name = \"fix\", prompt = \"prompts/work.md\", model = \"m\")\n")
	finishedRun(t, harness)

	// One second is past the retention of a nanosecond and inside the lease of the claim, which a clock an hour ahead would end.
	harness.opts.Retention = time.Nanosecond
	later := harness.deps()
	later.Clock.Wall = func() time.Time { return time.Now().Add(time.Second) }

	engine, err := NewEngine(t.Context(), harness.opts, later)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err = engine.Run(testItem, testProject)
	require.NoError(t, err)

	rows := spendRows(t, engine)
	require.Len(t, rows, 2, "the run before the retention and the run after it each wrote a row")
	assert.Equal(t, rows[0].Run, rows[1].Run, "the retention made the next run take the id of the deleted run")
	assert.NotEqual(t, rows[0].Call, rows[1].Call, "the execution id, not the run id, gives a row its identity")
}

func TestSpend_AnAgentStepWritesOneRowWithTheCostOfTheRun(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", costing(1.25))
	engine := rig.h.engine(t)

	_, err := engine.RunFile(testItem, testProject, flowFile(t, costFlow, "x"), RunSandbox(rig.sandbox))
	require.NoError(t, err)

	rows := spendRows(t, engine)
	require.Len(t, rows, 1)
	assert.Equal(t, runID(testItem, 1), rows[0].Tree)
	assert.Equal(t, runID(testItem, 1), rows[0].Run)
	assert.Equal(t, testItem, rows[0].Item)
	assert.Equal(t, "agent:a#1", rows[0].Name)
	assert.Equal(t, "sess-1", rows[0].Session)
	assert.Equal(t, sql.NullFloat64{Float64: 1.25, Valid: true}, rows[0].Cost)
}

func TestSpend_ARunWhoseCostIsUnknownLeavesTheCostOfItsRowNull(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	unpriced := exited
	unpriced.Unknown = true
	rig.reply("", unpriced)
	engine := rig.h.engine(t)

	_, err := engine.RunFile(testItem, testProject, flowFile(t, costFlow, "x"), RunSandbox(rig.sandbox))
	require.NoError(t, err)

	rows := spendRows(t, engine)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].Cost.Valid, "an unknown cost is NULL, so the budget check fails instead of counting 0")
}

func TestSpend_ANewTryOfACallWritesARowThatIsNotAnotherLane(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("{}", exited)
	engine := rig.h.engine(t)

	_, err := engine.RunFile(testItem, testProject, flowFile(t, outputsFlow("2"), "Write to {{ .output_file }}\n"), RunSandbox(rig.sandbox))
	require.Error(t, err, "no try writes the output")

	rows := spendRows(t, engine)
	require.Len(t, rows, 2, "each try is one agent execution")
	assert.Equal(t, "agent:a#1", rows[0].Name)
	assert.Equal(t, "agent:a#1"+ledger.RerunMark, rows[1].Name)

	count, err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).countEarlier(t.Context(), "a")
	require.NoError(t, err)
	assert.Equal(t, 1, count, "the two tries are one lane")
}

func TestStartSpend_AnAgentThatHasNotExitedLeavesARowWithAnUnknownCost(t *testing.T) {
	t.Parallel()

	engine := newAgentRig(t).h.opened(t)

	execution, err := engine.startSpend(t.Context(), PhaseInput{Run: "r", Item: "12", Call: "agent:x#1"})
	require.NoError(t, err)

	rows := spendRows(t, engine)
	require.Len(t, rows, 1)
	assert.Equal(t, execution, rows[0].Call)
	assert.False(t, rows[0].Cost.Valid, "a crash before the exit of the agent leaves the cost unknown")
}

func TestTreeCap_AnAgentOfASiblingThatRunsNowAddsNothingAndDoesNotFailTheCheck(t *testing.T) {
	t.Parallel()

	engine := newAgentRig(t).h.opened(t)
	engine.maxTreeBudget = 5

	_, err := engine.startSpend(t.Context(), PhaseInput{Run: runID("1", 1), Tree: runID("1", 1), Item: "1", Call: "agent:x#1"})
	require.NoError(t, err)

	got, err := engine.checkCaps("q", runID("1", 1))

	require.NoError(t, err, "the cost of a running agent is still to come, so it is not an unknown cost")
	assert.Nil(t, got.Stop)
}

func TestStartSpend_AContextOfTheEngineThatIsNoStepStopsThePhaseBeforeTheAgentStarts(t *testing.T) {
	t.Parallel()

	engine := newAgentRig(t).h.opened(t)

	_, err := engine.startSpend(engine.ctx, PhaseInput{Run: "r", Item: "12", Call: "agent:x#1"})

	require.ErrorContains(t, err, "read the step number of the agent step agent:x#1 of item 12")
	assert.Empty(t, spendRows(t, engine), "a phase that stops before the agent starts writes no row")
}

func TestStartSpend_ALedgerThatRefusesTheRowStopsThePhaseBeforeTheAgentStarts(t *testing.T) {
	t.Parallel()

	engine := newAgentRig(t).h.opened(t)
	closed, err := ledger.Open(t.Context(), t.TempDir())
	require.NoError(t, err)
	require.NoError(t, closed.Close())

	engine.ledger = closed

	_, err = engine.startSpend(t.Context(), PhaseInput{Run: "r", Item: "12", Call: "agent:x#1"})

	require.ErrorContains(t, err, "the agent step agent:x#1 of item 12 does not start")
}

func TestEndSpend_AFailedWriteOfTheCostIsLoggedAndTheRowKeepsItsUnknownCost(t *testing.T) {
	t.Parallel()

	engine := newAgentRig(t).h.opened(t)

	var log bytes.Buffer

	engine.logger = newLogger(&log, runRedactor(nil))

	execution, err := engine.startSpend(t.Context(), PhaseInput{Run: "r", Item: "12", Call: "agent:x#1"})
	require.NoError(t, err)

	_, err = engine.ledger.DB().ExecContext(t.Context(), `DROP TABLE spend`)
	require.NoError(t, err)

	engine.endSpend(t.Context(), execution, PhaseInput{Item: "12", Call: "agent:x#1"}, PhaseOutput{})

	assert.Contains(t, log.String(), "cannot set the cost of the agent call")
}
