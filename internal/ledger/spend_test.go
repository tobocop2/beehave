package ledger

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var epoch = time.UnixMilli(1_700_000_000_000)

func aCall(id, tree, item, name string, at time.Time) Call {
	return Call{ID: id, Tree: tree, Run: tree, Step: 4, Item: item, PullRequest: 0, Name: name, At: at}
}

func TestStartCall_WritesARowWithAnUnknownCostAndEndCallSetsTheCostAndTheSession(t *testing.T) {
	t.Parallel()

	held := openLedger(t, t.TempDir())

	require.NoError(t, held.StartCall(t.Context(), aCall("c1", "t", "12", "agent:fix#1", epoch)))

	var (
		cost    sql.NullFloat64
		session string
		step    int
		at      int64
	)

	read := func() {
		require.NoError(t, held.DB().QueryRowContext(t.Context(), `SELECT cost, session, step, at FROM spend WHERE call = 'c1'`).Scan(&cost, &session, &step, &at))
	}

	read()
	assert.False(t, cost.Valid, "a call that started and has not ended has no cost")
	assert.Empty(t, session)
	assert.Equal(t, 4, step)
	assert.Equal(t, epoch.UnixMilli(), at)

	require.NoError(t, held.EndCall(t.Context(), "c1", "sess", sql.NullFloat64{Float64: 1.5, Valid: true}))

	read()
	assert.True(t, cost.Valid)
	assert.InDelta(t, 1.5, cost.Float64, 0)
	assert.Equal(t, "sess", session)

	require.NoError(t, held.EndCall(t.Context(), "c1", "sess", sql.NullFloat64{}))

	read()
	assert.False(t, cost.Valid, "an end with no valid cost leaves the cost unknown")
}

func TestStartCall_ASecondRowOfOneExecutionIdIsRefusedAndTwoExecutionsOfOneRunAndStepAreKept(t *testing.T) {
	t.Parallel()

	held := openLedger(t, t.TempDir())

	require.NoError(t, held.StartCall(t.Context(), aCall("c1", "t", "12", "agent:fix#1", epoch)))
	require.NoError(t, held.StartCall(t.Context(), aCall("c2", "t", "12", "agent:fix#1", epoch)), "a rewind or a reused run id repeats the run and the step")
	require.ErrorContains(t, held.StartCall(t.Context(), aCall("c1", "t", "12", "agent:fix#1", epoch)), "record the agent call c1 in the ledger")
}

func TestTreeSpend_SumsTheCostOfOneTreeAndMarksAnUnknownCost(t *testing.T) {
	t.Parallel()

	held := openLedger(t, t.TempDir())
	ctx := t.Context()

	empty, err := held.TreeSpend(ctx, "t")
	require.NoError(t, err)
	assert.Equal(t, Sum{}, empty, "a tree with no row costs nothing and is known")

	for _, id := range []string{"c1", "c2"} {
		require.NoError(t, held.StartCall(ctx, aCall(id, "t", "12", "agent:fix#1", epoch)))
		require.NoError(t, held.EndCall(ctx, id, "s", sql.NullFloat64{Float64: 2, Valid: true}))
	}

	require.NoError(t, held.StartCall(ctx, aCall("c3", "other", "13", "agent:fix#1", epoch)))
	require.NoError(t, held.EndCall(ctx, "c3", "s", sql.NullFloat64{Float64: 50, Valid: true}))

	got, err := held.TreeSpend(ctx, "t")
	require.NoError(t, err)
	assert.Equal(t, Sum{Dollars: 4}, got, "the row of the other tree is not in the sum")

	require.NoError(t, held.StartCall(ctx, aCall("c4", "t", "12", "agent:fix#2", epoch)))

	got, err = held.TreeSpend(ctx, "t")
	require.NoError(t, err)
	assert.Equal(t, Sum{Dollars: 4}, got, "a call that runs in this process has no cost yet and adds 0")

	require.NoError(t, held.EndCall(ctx, "c4", "s", sql.NullFloat64{}))

	got, err = held.TreeSpend(ctx, "t")
	require.NoError(t, err)
	assert.Equal(t, Sum{Dollars: 4, Unknown: true}, got, "a call that ended with no cost is unknown")
}

func TestSpendSince_ACallOfAnEarlierProcessWithNoCostIsUnknownAndACallThatRunsAddsZero(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	earlier := openLedger(t, dir)
	require.NoError(t, earlier.StartCall(t.Context(), aCall("crashed", "t", "12", "agent:fix#1", epoch)))

	held := openLedger(t, dir)
	require.NoError(t, held.StartCall(t.Context(), aCall("running", "u", "13", "agent:fix#1", epoch)))

	got, err := held.SpendSince(t.Context(), epoch)
	require.NoError(t, err)
	assert.Equal(t, Sum{Unknown: true}, got, "the call that this process runs adds 0, and the one that no process runs is unknown")
}

func TestSpendSince_CountsTheCallsThatStartedAtTheStartOfTheWindowOrLater(t *testing.T) {
	t.Parallel()

	held := openLedger(t, t.TempDir())
	ctx := t.Context()

	for id, at := range map[string]time.Time{"old": epoch.Add(-time.Millisecond), "edge": epoch, "new": epoch.Add(time.Hour)} {
		require.NoError(t, held.StartCall(ctx, aCall(id, id, id, "agent:fix#1", at)))
		require.NoError(t, held.EndCall(ctx, id, "s", sql.NullFloat64{Float64: 1, Valid: true}))
	}

	got, err := held.SpendSince(ctx, epoch)
	require.NoError(t, err)
	assert.Equal(t, Sum{Dollars: 2}, got, "a call that started one millisecond before the window is out, and one at its start is in")
}

func TestCountCalls_CountsTheExecutionsOfOneItemByNamePrefixAndLeavesOutTheSkipped(t *testing.T) {
	t.Parallel()

	held := openLedger(t, t.TempDir())
	ctx := t.Context()

	for _, call := range []Call{
		aCall("c1", "t", "12", "agent:fix#1", epoch),
		aCall("c2", "t", "12", "agent:fix#1", epoch),
		aCall("c3", "t", "12", "agent:fix#2", epoch),
		aCall("c4", "t", "12", "agent:fix-scoped-1#1", epoch),
		aCall("c5", "t", "13", "agent:fix#1", epoch),
		aCall("c6", "t", "12", "agent:fix#1"+RerunMark, epoch),
	} {
		require.NoError(t, held.StartCall(ctx, call))
	}

	count, err := held.CountCalls(ctx, "12", "agent:fix#", nil)
	require.NoError(t, err)
	assert.Equal(t, 3, count, "two executions of fix#1 and one of fix#2 count, and neither a rerun, nor fix-scoped-1, nor the other item does")

	count, err = held.CountCalls(ctx, "12", "agent:fix#", []string{"c2", "c3", "unknown"})
	require.NoError(t, err)
	assert.Equal(t, 1, count, "the skipped executions are left out")

	count, err = held.CountCalls(ctx, "12", "agent:nothing#", nil)
	require.NoError(t, err)
	assert.Zero(t, count)
}

func TestSpend_EveryReadAndWriteNamesItsFailureWhenTheLedgerIsClosed(t *testing.T) {
	t.Parallel()

	held := openLedger(t, t.TempDir())
	require.NoError(t, held.Close())

	ctx := t.Context()

	_, err := held.TreeSpend(ctx, "t")
	require.ErrorContains(t, err, "sum the spend in the ledger")

	_, err = held.SpendSince(ctx, epoch)
	require.ErrorContains(t, err, "sum the spend in the ledger")

	_, err = held.CountCalls(ctx, "12", "agent:fix#", nil)
	require.ErrorContains(t, err, "count the agent calls of item 12 in the ledger")

	require.ErrorContains(t, held.EndCall(ctx, "c1", "s", sql.NullFloat64{}), "set the cost of the agent call c1 in the ledger")
}
