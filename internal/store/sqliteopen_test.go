package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/store"
)

const testFile = "test.db"

func testSpec() store.SQLiteFile {
	return store.SQLiteFile{Name: testFile, Schema: `CREATE TABLE IF NOT EXISTS t (x INTEGER); PRAGMA user_version = 1;`, Version: 1, Owned: []string{"t"}, BusyMs: 1000}
}

func openTest(t *testing.T, dir string) *sql.DB {
	t.Helper()

	db, err := store.OpenSQLite(t.Context(), dir, testSpec())
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	return db
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err, path)

	return info.Mode().Perm()
}

func rawExec(t *testing.T, path, statement string) {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)

	_, err = db.ExecContext(t.Context(), statement)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

func TestOpenSQLite_MakesTheFileAndItsWalAndSharedMemoryFilesWithModeZeroSixHundred(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	db := openTest(t, dir)

	_, err := db.ExecContext(t.Context(), `INSERT INTO t (x) VALUES (1)`)
	require.NoError(t, err)

	for _, name := range []string{testFile, testFile + "-wal", testFile + "-shm"} {
		assert.Equal(t, os.FileMode(0o600), modeOf(t, filepath.Join(dir, name)), name)
	}
}

func TestOpenSQLite_SetsModeZeroSixHundredOnAnExistingFileAndItsWalAndSharedMemoryFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := openTest(t, dir)

	_, err := first.ExecContext(t.Context(), `INSERT INTO t (x) VALUES (1)`)
	require.NoError(t, err)

	for _, name := range []string{testFile, testFile + "-wal", testFile + "-shm"} {
		require.NoError(t, os.Chmod(filepath.Join(dir, name), 0o644), name) //nolint:gosec // G302: the test makes a file wider than the product allows
	}

	openTest(t, dir)

	for _, name := range []string{testFile, testFile + "-wal", testFile + "-shm"} {
		assert.Equal(t, os.FileMode(0o600), modeOf(t, filepath.Join(dir, name)), name)
	}
}

func TestOpenSQLite_RefusesASymbolicLinkAtThePathAndWritesNothingToItsTarget(t *testing.T) {
	t.Parallel()

	dir, elsewhere := t.TempDir(), t.TempDir()
	target := filepath.Join(elsewhere, "target.db")
	require.NoError(t, os.WriteFile(target, nil, 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, testFile)))

	_, err := store.OpenSQLite(t.Context(), dir, testSpec())
	require.ErrorContains(t, err, "symbolic link")
	require.ErrorContains(t, err, "Remove the link")

	info, statErr := os.Stat(target)
	require.NoError(t, statErr)
	assert.Zero(t, info.Size(), "the target of the link stays empty")
}

func TestOpenSQLite_RefusesASymbolicLinkAtTheWalPath(t *testing.T) {
	t.Parallel()

	dir, elsewhere := t.TempDir(), t.TempDir()
	target := filepath.Join(elsewhere, "target-wal")
	require.NoError(t, os.WriteFile(target, nil, 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, testFile+"-wal")))

	_, err := store.OpenSQLite(t.Context(), dir, testSpec())
	require.ErrorContains(t, err, testFile+"-wal is a symbolic link")
}

func TestOpenSQLite_RefusesAFileOfAnotherSchemaVersionAndNamesTheFix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rawExec(t, filepath.Join(dir, testFile), `PRAGMA user_version = 99`)

	_, err := store.OpenSQLite(t.Context(), dir, testSpec())
	require.ErrorContains(t, err, "schema version 99, and this build knows version 1")
	require.ErrorContains(t, err, "move test.db away")
}

func TestOpenSQLite_RefusesANewFileThatHoldsATableOfAnotherOwnerAndNamesTheFix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rawExec(t, filepath.Join(dir, testFile), `CREATE TABLE other (x INTEGER)`)

	_, err := store.OpenSQLite(t.Context(), dir, testSpec())
	require.ErrorContains(t, err, `holds "other", which beehave does not own`)
	require.ErrorContains(t, err, "Move test.db away")
}

func TestOpenSQLite_TakesAFileOfVersionZeroThatHoldsOnlyItsOwnTables(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rawExec(t, filepath.Join(dir, testFile), `CREATE TABLE t (x INTEGER)`)

	db := openTest(t, dir)

	var version int

	require.NoError(t, db.QueryRowContext(t.Context(), `PRAGMA user_version`).Scan(&version))
	assert.Equal(t, 1, version)
}

func TestOpenSQLite_RunsTheSchemaInOneTransactionSoAFailureLeavesNoTable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := testSpec()
	spec.Schema = `CREATE TABLE t (x INTEGER); SELECT y FROM missing; PRAGMA user_version = 1;`

	_, err := store.OpenSQLite(t.Context(), dir, spec)
	require.ErrorContains(t, err, "no such table")

	db, err := sql.Open("sqlite", filepath.Join(dir, testFile))
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	var tables int

	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master`).Scan(&tables))
	assert.Zero(t, tables, "the table of the first statement is rolled back")
}

func TestOpenSQLite_RefusesAFileThatIsNotADatabaseAndNamesTheFix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, testFile), []byte("this is not a database file, it is longer than a header"), 0o600))

	_, err := store.OpenSQLite(t.Context(), dir, testSpec())
	require.ErrorContains(t, err, "cannot open test.db")
	require.ErrorContains(t, err, "move test.db away")
}

func TestOpenSQLite_RefusesAFolderThatDoesNotExist(t *testing.T) {
	t.Parallel()

	_, err := store.OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "missing"), testSpec())
	require.ErrorContains(t, err, "cannot create test.db")
}

func TestOpenSQLite_RefusesAPathThatIsAFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, testFile), 0o700))

	_, err := store.OpenSQLite(t.Context(), dir, testSpec())
	require.ErrorContains(t, err, "cannot create test.db")
}

func TestOpenSQLite_RefusesAFileNameThatTheSystemCannotRead(t *testing.T) {
	t.Parallel()

	spec := testSpec()
	spec.Name = strings.Repeat("a", 300)

	_, err := store.OpenSQLite(t.Context(), t.TempDir(), spec)
	require.ErrorContains(t, err, "cannot create")
}

func TestOpenSQLite_RefusesACancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := store.OpenSQLite(ctx, t.TempDir(), testSpec())
	require.ErrorContains(t, err, "cannot open test.db")
}
