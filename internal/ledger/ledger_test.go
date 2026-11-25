package ledger

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const insertSpend = `INSERT INTO spend (call, tree, run_id, step, session, item, pull_request, call_name, cost, at) VALUES (?, 't', 'item-1-r1', 3, 's', '1', 7, 'fix', ?, 1000)`

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

func openLedger(t *testing.T, dir string) *Ledger {
	t.Helper()

	got, err := Open(t.Context(), dir)
	require.NoError(t, err)

	t.Cleanup(func() { _ = got.Close() })

	return got
}

func TestOpen_MakesLedgerDbWithModeZeroSixHundredAndTheThreeTables(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	got := openLedger(t, dir)

	info, err := os.Stat(filepath.Join(dir, "ledger.db"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Equal(t, []string{"round_items", "rounds", "spend"}, tableNames(t, got.DB()))
}

func TestOpen_SetsModeZeroSixHundredOnALedgerThatExistsWithAWiderMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := openLedger(t, dir)

	_, err := first.DB().ExecContext(t.Context(), insertSpend, "c1", 0.25)
	require.NoError(t, err)

	for _, name := range []string{"ledger.db", "ledger.db-wal", "ledger.db-shm"} {
		require.NoError(t, os.Chmod(filepath.Join(dir, name), 0o644), name) //nolint:gosec // G302: the test makes a file wider than the product allows
	}

	openLedger(t, dir)

	for _, name := range []string{"ledger.db", "ledger.db-wal", "ledger.db-shm"} {
		info, statErr := os.Stat(filepath.Join(dir, name))
		require.NoError(t, statErr, name)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), name)
	}
}

func TestOpen_RefusesASymbolicLinkAtTheLedgerPath(t *testing.T) {
	t.Parallel()

	dir, elsewhere := t.TempDir(), t.TempDir()
	target := filepath.Join(elsewhere, "target.db")
	require.NoError(t, os.WriteFile(target, nil, 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "ledger.db")))

	_, err := Open(t.Context(), dir)
	require.ErrorContains(t, err, "ledger.db is a symbolic link")

	info, statErr := os.Stat(target)
	require.NoError(t, statErr)
	assert.Zero(t, info.Size())
}

func TestOpen_AReopenReadsTheRowsThatTheFirstOpenWrote(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	first, err := Open(t.Context(), dir)
	require.NoError(t, err)

	_, err = first.DB().ExecContext(t.Context(), insertSpend, "c1", 0.25)
	require.NoError(t, err)

	_, err = first.DB().ExecContext(t.Context(), insertSpend, "c2", nil)
	require.NoError(t, err)
	require.NoError(t, first.Close())

	second := openLedger(t, dir)

	var (
		count int
		known sql.NullFloat64
	)

	require.NoError(t, second.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM spend`).Scan(&count))
	assert.Equal(t, 2, count)
	require.NoError(t, second.DB().QueryRowContext(t.Context(), `SELECT cost FROM spend WHERE call = 'c2'`).Scan(&known))
	assert.False(t, known.Valid, "a NULL cost stays unknown")
}

func TestOpen_ASecondRowOfOneRunAndStepIsKeptAndASecondRowOfOneExecutionIsRefused(t *testing.T) {
	t.Parallel()

	got := openLedger(t, t.TempDir())

	_, err := got.DB().ExecContext(t.Context(), insertSpend, "c1", 1)
	require.NoError(t, err)

	_, err = got.DB().ExecContext(t.Context(), insertSpend, "c2", 1)
	require.NoError(t, err, "a run id and a step number repeat, and each call is still a row")

	_, err = got.DB().ExecContext(t.Context(), insertSpend, "c1", 1)
	require.ErrorContains(t, err, "UNIQUE")
}

func TestOpen_RefusesALedgerOfASchemaVersionItDoesNotKnow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	other, err := sql.Open("sqlite", filepath.Join(dir, "ledger.db"))
	require.NoError(t, err)

	_, err = other.ExecContext(t.Context(), `PRAGMA user_version = 99`)
	require.NoError(t, err)
	require.NoError(t, other.Close())

	_, err = Open(t.Context(), dir)
	require.ErrorContains(t, err, "schema version 99")
}

func TestOpen_RefusesANewFileThatHoldsATableOfAnotherOwner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	other, err := sql.Open("sqlite", filepath.Join(dir, "ledger.db"))
	require.NoError(t, err)

	_, err = other.ExecContext(t.Context(), `CREATE TABLE notes (x INTEGER)`)
	require.NoError(t, err)
	require.NoError(t, other.Close())

	_, err = Open(t.Context(), dir)
	require.ErrorContains(t, err, `holds "notes"`)
	require.ErrorContains(t, err, "ledger.db")
}

func TestOpen_RefusesAStateFolderThatDoesNotExist(t *testing.T) {
	t.Parallel()

	_, err := Open(t.Context(), filepath.Join(t.TempDir(), "missing"))
	require.ErrorContains(t, err, "cannot create ledger.db")
}
