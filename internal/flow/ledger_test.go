package flow

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ledgerRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()

	var count int

	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count))
	return count
}

func requireNoLedgerSidecars(t *testing.T, dir string) {
	t.Helper()

	for _, name := range []string{"ledger.db-wal", "ledger.db-shm"} {
		_, err := os.Stat(filepath.Join(dir, name))
		require.ErrorIs(t, err, os.ErrNotExist, "%s stays after the last connection closes", name)
	}
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
	require.ErrorContains(t, err, "open the state folder")
}

func TestNewEngine_AWorkflowStoreThatCannotBeOpenedStopsTheEngineAndClosesTheLedger(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	require.NoError(t, os.MkdirAll(harness.opts.StateDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(harness.opts.StateDir, "dbos.db"), []byte("this is not a database file, it is longer than a header"), 0o600))

	_, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.ErrorContains(t, err, "open the workflow store")
	requireNoLedgerSidecars(t, harness.opts.StateDir)
}

func TestShutdown_ClosesTheLedger(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)

	_, err = engine.ledger.DB().ExecContext(t.Context(), `INSERT INTO rounds (base, round, status, last_pass, integration_pr, build_version, step_versions) VALUES ('b', 1, 'open', 0, 0, 'v', '{}')`)
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())
	requireNoLedgerSidecars(t, engine.dir)
}

func TestLaunch_TheLedgerKeepsItsRowsWhenTheRetentionDeletesEveryRunOlderThanTheRetention(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)

	first, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	require.NoError(t, first.Launch())

	_, err = first.Run(testItem, testProject)
	require.NoError(t, err)

	_, err = first.ledger.DB().ExecContext(t.Context(), `INSERT INTO spend (call, tree, run_id, step, session, item, pull_request, call_name, cost, at) VALUES ('c1', 't', ?, 1, 's', ?, 7, 'fix', 0.5, 1000)`, runID(testItem, 1), testItem)
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
