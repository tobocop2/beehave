package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedSpend inserts one spend row into the ledger of the engine, as an agent step of an earlier process wrote it. A nil cost is an unknown cost.
func seedSpend(t *testing.T, engine *Engine, call, tree, item string, at time.Time, cost any) {
	t.Helper()

	_, err := engine.ledger.DB().ExecContext(t.Context(),
		`INSERT INTO spend (call, tree, run_id, step, session, item, pull_request, call_name, cost, at) VALUES (?, ?, ?, 1, 's', ?, 0, 'agent:fix#1', ?, ?)`,
		call, tree, tree, item, cost, at.UnixMilli())
	require.NoError(t, err)
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

	count, err := (&script{e: engine, ctx: engine.ctx, in: ItemInput{Item: testItem}}).countEarlier("fix")

	require.NoError(t, err)
	assert.Equal(t, 1, count, "the fix call of the deleted run still counts")
}
