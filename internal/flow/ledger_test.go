package flow

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ledger"
)

const seedSpend = `INSERT INTO spend (tree, run_id, step, session, item, pull_request, call_name, cost, at) VALUES ('t', $1, 1, 's', '1', 7, 'fix', 0.5, 1000)`

func ledgerRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()

	var count int

	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count))
	return count
}

func TestNewEngine_MakesLedgerDbWithModeZeroSixHundredAndTheThreeTables(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	info, err := os.Stat(filepath.Join(engine.dir, "ledger.db"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	for _, table := range []string{"rounds", "round_items", "spend"} {
		assert.Zero(t, ledgerRows(t, engine.ledger.DB(), table), table)
	}
}

func TestNewEngine_ALedgerFileThatCannotBeOpenedStopsTheEngine(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	require.NoError(t, os.MkdirAll(filepath.Join(harness.opts.StateDir, "ledger.db"), 0o700))

	_, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.ErrorContains(t, err, "ledger.db")
}

func TestNewEngine_AWorkflowStoreThatCannotBeOpenedStopsTheEngineAndClosesTheLedger(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	require.NoError(t, os.MkdirAll(harness.opts.StateDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(harness.opts.StateDir, "dbos.db"), []byte("this is not a database file, it is longer than a header"), 0o600))

	_, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.ErrorContains(t, err, "open the workflow store")
}

func TestNewEngine_ALedgerWhoseTransactionTableCannotBeMigratedStopsTheEngine(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	require.NoError(t, os.MkdirAll(harness.opts.StateDir, 0o700))

	held, err := ledger.Open(t.Context(), harness.opts.StateDir)
	require.NoError(t, err)

	_, err = held.DB().ExecContext(t.Context(), `CREATE TABLE dbos_transaction_completion_migrations (other TEXT)`)
	require.NoError(t, err)
	require.NoError(t, held.Close())

	_, err = NewEngine(t.Context(), harness.opts, harness.deps())
	require.ErrorContains(t, err, "open the data source of the ledger")
}

func TestLaunch_TheLedgerKeepsItsRowsWhenTheRetentionDeletesEveryRunOlderThanTheRetention(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	_, err = first.Run(testItem, testProject)
	require.NoError(t, err)

	_, err = first.ledger.DB().ExecContext(t.Context(), `INSERT INTO spend (tree, run_id, step, session, item, pull_request, call_name, cost, at) VALUES ('t', ?, 1, 's', ?, 7, 'fix', 0.5, 1000)`, runID(testItem, 1), testItem)
	require.NoError(t, err)
	require.NoError(t, first.Shutdown())

	client := harness.client(t, testVersion)

	rows, err := client.Status()
	require.NoError(t, err)
	require.Len(t, rows, 1, "the run is there before the retention runs")

	deps := harness.deps()
	deps.Clock.Retain = time.Nanosecond
	deps.Clock.Wall = func() time.Time { return time.Now().Add(time.Hour) }

	second, err := NewEngine(t.Context(), harness.opts, deps)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, second.Shutdown()) })
	require.NoError(t, second.Launch())

	rows, err = client.Status()
	require.NoError(t, err)
	assert.Empty(t, rows, "the retention deleted every run")
	assert.Equal(t, 1, ledgerRows(t, second.ledger.DB(), "spend"), "the spend row of the deleted run stays")
}

func TestRunAsTransaction_ARowWrittenToTheLedgerRunsOnceAndIsReadBackAfterARestart(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)

	spend := func(ctx dbos.Context, run string) (string, error) {
		_, txErr := dbos.RunAsTransaction(ctx, first.source, func(ctx context.Context, tx dbos.Tx) (int64, error) {
			res, execErr := tx.Exec(ctx, seedSpend, run)
			if execErr != nil {
				return 0, execErr
			}

			return res.RowsAffected()
		})

		return run, txErr
	}

	dbos.RegisterWorkflow(first.ctx, spend, dbos.WithWorkflowName("ledger-spend"))
	require.NoError(t, first.Launch())

	for range 2 {
		handle, runErr := dbos.RunWorkflow(first.ctx, spend, "ledger-run-1", dbos.WithWorkflowID("ledger-spend-1"))
		require.NoError(t, runErr)

		_, runErr = handle.GetResult()
		require.NoError(t, runErr, "a second start with the same id returns the first result")
	}

	assert.Equal(t, 1, ledgerRows(t, first.ledger.DB(), "spend"))
	assert.Equal(t, 1, ledgerRows(t, first.ledger.DB(), "transaction_completion"), "DBOS records the step in the ledger file, in the same transaction as the row")
	require.NoError(t, first.Shutdown())

	second, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, second.Shutdown()) })

	assert.Equal(t, 1, ledgerRows(t, second.ledger.DB(), "spend"), "the restart reads the row that the first engine wrote")
}
