package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()

	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })

	return root
}

func internalSpec() SQLiteFile {
	return SQLiteFile{Name: "test.db", Schema: `CREATE TABLE IF NOT EXISTS t (x INTEGER); PRAGMA user_version = 1;`, Version: 1, Owned: []string{"t"}, BusyMs: 1000}
}

func TestSecure_SkipsAFileThatIsGone(t *testing.T) {
	t.Parallel()

	require.NoError(t, secure(openTestRoot(t, t.TempDir()), "gone.db-shm"))
}

func TestSecure_KeepsAnErrorThatIsNotAMissingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plain"), nil, 0o600))

	err := secure(openTestRoot(t, dir), "plain/inside-shm")
	require.ErrorIs(t, err, syscall.ENOTDIR)
}

func TestMakeWalFile_RefusesAFileWhoseFolderIsGone(t *testing.T) {
	t.Parallel()

	file := internalSpec()
	file.Name = "missing/file.db"

	require.ErrorIs(t, makeWalFile(t.Context(), openTestRoot(t, t.TempDir()), file), fs.ErrNotExist)
}

func TestPreparePrivate_KeepsAnErrorOfTheDriver(t *testing.T) {
	t.Parallel()

	require.ErrorContains(t, preparePrivate(t.Context(), filepath.Join(t.TempDir(), "missing", "test.db"), internalSpec()), "unable to open")
}

func TestLinkOnce_KeepsAnErrorThatIsNotAnExistingName(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, linkOnce(openTestRoot(t, t.TempDir()), "gone.db.new-x", "test.db"), fs.ErrNotExist)
}

func TestOpenSQLite_ALinkThatLostTheRaceKeepsTheFileOfTheWinner(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := internalSpec()

	winner, err := OpenSQLite(t.Context(), dir, file)
	require.NoError(t, err)

	_, err = winner.ExecContext(t.Context(), `INSERT INTO t (x) VALUES (7)`)
	require.NoError(t, err)
	require.NoError(t, winner.Close())

	// The loser found no file, and the winner linked its file before the loser could.
	require.NoError(t, makeWalFile(t.Context(), openTestRoot(t, dir), file))

	db, err := OpenSQLite(t.Context(), dir, file)
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	var kept int

	require.NoError(t, db.QueryRowContext(t.Context(), `SELECT x FROM t`).Scan(&kept))
	assert.Equal(t, 7, kept, "the row of the winner")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, entry := range entries {
		assert.NotContains(t, entry.Name(), privateInfix, "a private name that the loser left")
	}
}
