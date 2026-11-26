package store_test

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
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

func TestOpenSQLite_DoesNotFailAtTheModeStepWhenAnotherProcessRemovesTheSharedMemoryFile(t *testing.T) {
	t.Parallel()

	const opens = 1000

	dir := t.TempDir()
	shm := filepath.Join(dir, testFile+"-shm")
	openTest(t, dir)

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)

		for {
			select {
			case <-stop:
				return
			default:
			}

			// The remover stands for the last close of another process. The remove may meet a file that SQLite already took away.
			assert.NoError(t, os.WriteFile(shm, nil, 0o600))
			_ = os.Remove(shm)
		}
	}()

	var failures []error

	for range opens {
		db, err := store.OpenSQLite(t.Context(), dir, testSpec())
		// SQLite itself reports other errors when its own -shm file is removed under it; only the mode step is under test.
		if errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, err)
		}

		if err == nil {
			require.NoError(t, db.Close())
		}
	}

	close(stop)
	<-done

	assert.Empty(t, failures, "opens whose mode step met a file that was gone")
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

func TestOpenSQLite_RefusesAFileOfANewerVersionThatHoldsEveryOwnedTable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	rawExec(t, filepath.Join(dir, testFile), `CREATE TABLE t (x INTEGER); PRAGMA user_version = 99`)

	_, err := store.OpenSQLite(t.Context(), dir, testSpec())
	require.ErrorContains(t, err, "schema version 99, and this build knows version 1")
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

func TestOpenSQLite_AFolderThatCannotBeWrittenNamesItsOwnCauseAndNeverTellsTheOwnerToMoveTheFile(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("a process of the root user writes to a read-only folder")
	}

	dir := t.TempDir()
	require.NoError(t, openTest(t, dir).Close())
	require.NoError(t, exectest.ChmodFolder(dir, 0o500))
	t.Cleanup(func() { require.NoError(t, exectest.ChmodFolder(dir, 0o700)) })

	_, err := store.OpenSQLite(t.Context(), dir, testSpec())
	require.ErrorContains(t, err, "cannot open test.db")
	assert.NotContains(t, err.Error(), "move", "a permission error is not a damaged file")
	assert.NotContains(t, err.Error(), "away")
}

// widerSpec is testSpec with a second table, as a later build that adds a table at the same version.
func widerSpec() store.SQLiteFile {
	spec := testSpec()
	spec.Schema = `CREATE TABLE IF NOT EXISTS t (x INTEGER); CREATE TABLE IF NOT EXISTS u (y INTEGER); PRAGMA user_version = 1;`
	spec.Owned = []string{"t", "u"}

	return spec
}

// holdWriteLock keeps a write transaction open on the test file of dir until the test ends.
func holdWriteLock(t *testing.T, dir string) {
	t.Helper()

	holder, err := sql.Open("sqlite", filepath.Join(dir, testFile))
	require.NoError(t, err)

	t.Cleanup(func() { _ = holder.Close() })

	conn, err := holder.Conn(t.Context())
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })

	_, err = conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`)
	require.NoError(t, err)
}

// firstReadWhenItAppears waits for the file at path and returns the text that a plain connection reads first with query.
func firstReadWhenItAppears(path, query string) string {
	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); err != nil {
			continue
		}

		db, err := sql.Open("sqlite", store.SQLiteDSN(path, 1000, nil))
		if err != nil {
			return err.Error()
		}

		var read string

		err = db.QueryRowContext(context.Background(), query).Scan(&read)
		if closeErr := db.Close(); err == nil {
			err = closeErr
		}

		if err != nil {
			return err.Error()
		}

		return read
	}

	return "the file did not appear"
}

func TestOpenSQLite_ConcurrentFirstOpensOfANewFileAllSucceed(t *testing.T) {
	t.Parallel()

	const folders, openers = 100, 12

	var (
		mu       sync.Mutex
		failures []error
	)

	for range folders {
		dir := t.TempDir()

		var wg sync.WaitGroup

		for range openers {
			wg.Go(func() {
				db, err := store.OpenSQLite(t.Context(), dir, testSpec())
				if err == nil {
					err = db.Close()
				}

				if err != nil {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
				}
			})
		}

		wg.Wait()
	}

	assert.Empty(t, failures, "first opens of a new file that failed")
}

func TestOpenSQLite_ANewFileIsInWalModeWhenItAppears(t *testing.T) {
	t.Parallel()

	const files = 20

	for range files {
		dir := t.TempDir()
		seen := make(chan string, 1)

		go func() { seen <- firstReadWhenItAppears(filepath.Join(dir, testFile), `PRAGMA journal_mode`) }()

		openTest(t, dir)
		assert.Equal(t, "wal", <-seen, "the journal mode that another opener reads when the file first appears")
	}
}

func TestOpenSQLite_ANewFileIsUpToDateWhenItAppears(t *testing.T) {
	t.Parallel()

	const (
		files = 20
		query = `SELECT (SELECT user_version FROM pragma_user_version) || ' ' || (SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 't')`
	)

	for range files {
		dir := t.TempDir()
		seen := make(chan string, 1)

		go func() { seen <- firstReadWhenItAppears(filepath.Join(dir, testFile), query) }()

		openTest(t, dir)
		assert.Equal(t, "1 1", <-seen, "the version and the count of tables that another opener reads when the file first appears, so it takes no write lock")
	}
}

func TestOpenSQLite_AnUpToDateFileTakesNoWriteLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, openTest(t, dir).Close())
	holdWriteLock(t, dir)

	spec := testSpec()
	spec.BusyMs = 50

	db, err := store.OpenSQLite(t.Context(), dir, spec)
	require.NoError(t, err, "a file at the current version with every table opens while another connection writes")
	require.NoError(t, db.Close())
}

func TestOpenSQLite_AnUpToDateFileThatLacksATableOfTheSchemaGetsTheTable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, openTest(t, dir).Close())

	db, err := store.OpenSQLite(t.Context(), dir, widerSpec())
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	var tables int

	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'u'`).Scan(&tables))
	assert.Equal(t, 1, tables, "the table that the file of version 1 lacked")
}

func TestOpenSQLite_ALockThatDoesNotEndNamesItsOwnCauseAndNeverTellsTheOwnerToMoveTheFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, openTest(t, dir).Close())
	holdWriteLock(t, dir)

	// The file lacks the table u, so the open must write and meets the lock.
	spec := widerSpec()
	spec.BusyMs = 50

	_, err := store.OpenSQLite(t.Context(), dir, spec)
	require.ErrorContains(t, err, "cannot open test.db")
	require.ErrorContains(t, err, "locked")
	assert.NotContains(t, err.Error(), "move", "a busy lock is not a damaged file")
	assert.NotContains(t, err.Error(), "away")
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

func TestOpenSQLite_RefusesAFileWhoseWalNameTheSystemCannotRead(t *testing.T) {
	t.Parallel()

	// The name fits the system's limit of 255 bytes, and the name of its -wal file does not.
	dir := t.TempDir()
	spec := testSpec()
	spec.Name = strings.Repeat("a", 252)
	require.NoError(t, os.WriteFile(filepath.Join(dir, spec.Name), nil, 0o600))

	_, err := store.OpenSQLite(t.Context(), dir, spec)
	require.ErrorContains(t, err, "cannot create "+spec.Name)
	require.ErrorIs(t, err, syscall.ENAMETOOLONG)
}

func TestOpenSQLite_RefusesACancelledContextAndLeavesNoFile(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	dir := t.TempDir()

	_, err := store.OpenSQLite(ctx, dir, testSpec())
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "cannot create test.db")
	assert.Empty(t, namesIn(t, dir), "a new file whose schema did not run is never put at its name")
}

func TestOpenSQLite_ASchemaThatFailsOnANewFileLeavesNoFileAndNamesTheCause(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	spec := testSpec()
	spec.Schema = `CREATE TABLE t (x INTEGER); SELECT y FROM missing; PRAGMA user_version = 1;`

	_, err := store.OpenSQLite(t.Context(), dir, spec)
	require.ErrorContains(t, err, "cannot create test.db")
	require.ErrorContains(t, err, "no such table: missing")
	assert.Empty(t, namesIn(t, dir), "a new file whose schema failed is never put at its name")
}

// namesIn returns the names of the entries of dir.
func namesIn(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}
