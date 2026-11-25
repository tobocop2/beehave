package ledger_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/ledger"
)

func tableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	require.NoError(t, err)

	defer func() { require.NoError(t, rows.Close()) }()

	var names []string

	for rows.Next() {
		var name string

		require.NoError(t, rows.Scan(&name))

		names = append(names, name)
	}

	require.NoError(t, rows.Err())

	return names
}

func TestOpen_MakesLedgerDbWithModeZeroSixHundredAndTheThreeTables(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	got, err := ledger.Open(t.Context(), dir)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, got.Close()) })

	info, err := os.Stat(filepath.Join(dir, "ledger.db"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Equal(t, []string{"round_items", "rounds", "spend"}, tableNames(t, got.DB()))
}

func TestOpen_GivesTheWalAndSharedMemoryFilesTheModeOfTheFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	got, err := ledger.Open(t.Context(), dir)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, got.Close()) })

	_, err = got.DB().ExecContext(t.Context(), `INSERT INTO rounds (base, round, status, last_pass, integration_pr, build_version, step_versions) VALUES ('main', 1, 'open', 0, 0, 'b', 's')`)
	require.NoError(t, err)

	for _, name := range []string{"ledger.db-wal", "ledger.db-shm"} {
		info, statErr := os.Stat(filepath.Join(dir, name))
		require.NoError(t, statErr, name)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), name)
	}
}

func TestOpen_AReopenReadsTheRowsThatTheFirstOpenWrote(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	first, err := ledger.Open(t.Context(), dir)
	require.NoError(t, err)

	_, err = first.DB().ExecContext(t.Context(), `INSERT INTO spend (tree, run_id, step, session, item, pull_request, call_name, cost, at) VALUES ('t', 'item-1-r1', 3, 's', '1', 7, 'fix', 0.25, 1000)`)
	require.NoError(t, err)

	_, err = first.DB().ExecContext(t.Context(), `INSERT INTO spend (tree, run_id, step, session, item, pull_request, call_name, cost, at) VALUES ('t', 'item-1-r1', 4, 's', '1', 7, 'fix', NULL, 2000)`)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second, err := ledger.Open(t.Context(), dir)
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, second.Close()) })

	var (
		count int
		known sql.NullFloat64
	)

	require.NoError(t, second.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM spend`).Scan(&count))
	assert.Equal(t, 2, count)
	require.NoError(t, second.DB().QueryRowContext(t.Context(), `SELECT cost FROM spend WHERE step = 4`).Scan(&known))
	assert.False(t, known.Valid, "a NULL cost stays unknown")
}

func TestOpen_ASecondSpendRowOfTheSameRunAndStepIsRefused(t *testing.T) {
	t.Parallel()

	got, err := ledger.Open(t.Context(), t.TempDir())
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, got.Close()) })

	const insert = `INSERT INTO spend (tree, run_id, step, session, item, pull_request, call_name, cost, at) VALUES ('t', 'r', 1, 's', 'i', 1, 'fix', 1, 1)`

	_, err = got.DB().ExecContext(t.Context(), insert)
	require.NoError(t, err)

	_, err = got.DB().ExecContext(t.Context(), insert)
	require.ErrorContains(t, err, "UNIQUE")
}

func TestOpen_RefusesAFileOfASchemaVersionItDoesNotKnow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	other, err := sql.Open("sqlite", filepath.Join(dir, "ledger.db"))
	require.NoError(t, err)

	_, err = other.ExecContext(t.Context(), `PRAGMA user_version = 99`)
	require.NoError(t, err)
	require.NoError(t, other.Close())

	_, err = ledger.Open(t.Context(), dir)
	require.ErrorContains(t, err, "schema version 99")
}

func TestOpen_RefusesAStateFolderThatDoesNotExist(t *testing.T) {
	t.Parallel()

	_, err := ledger.Open(t.Context(), filepath.Join(t.TempDir(), "missing"))
	require.ErrorContains(t, err, "cannot create ledger.db")
}

func TestOpen_RefusesAFileThatIsNotADatabase(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ledger.db"), []byte("this is not a database file, it is longer than a header"), 0o600))

	_, err := ledger.Open(t.Context(), dir)
	require.ErrorContains(t, err, "ledger")
}

func TestOpen_ACancelledContextFailsTheOpen(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := ledger.Open(ctx, t.TempDir())
	require.Error(t, err)
}
