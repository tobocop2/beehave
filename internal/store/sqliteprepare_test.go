package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
)

// countingConnector opens connections of the SQLite driver and counts each transaction that the connections begin.
type countingConnector struct {
	dsn    string
	begins *atomic.Int32
}

func (c countingConnector) Connect(context.Context) (driver.Conn, error) {
	inner, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}

	full, ok := inner.(fullConn)
	if !ok {
		return nil, errors.New("the driver connection lacks a method that takes a context")
	}

	return countingConn{fullConn: full, begins: c.begins}, nil
}

func (c countingConnector) Driver() driver.Driver { return &sqlite.Driver{} }

// fullConn is a driver connection with the methods that take a context.
type fullConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ExecerContext
	driver.QueryerContext
}

type countingConn struct {
	fullConn
	begins *atomic.Int32
}

func (c countingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.begins.Add(1)

	return c.fullConn.BeginTx(ctx, opts)
}

// countedBegins prepares the file in dir and returns the count of transactions that the preparation began.
func countedBegins(t *testing.T, dir string, file SQLiteFile) int32 {
	t.Helper()

	var begins atomic.Int32

	db := sql.OpenDB(countingConnector{dsn: walDSN(filepath.Join(dir, file.Name), file.BusyMs), begins: &begins})

	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, prepareSQLite(t.Context(), db, file))

	return begins.Load()
}

func TestPrepareSQLite_ReadsTheVersionAndRunsTheSchemaInOneTransaction(t *testing.T) {
	t.Parallel()

	file := SQLiteFile{Name: "one.db", Schema: `CREATE TABLE IF NOT EXISTS t (x INTEGER); PRAGMA user_version = 1;`, Version: 1, Owned: []string{"t"}, BusyMs: 1000}

	assert.Equal(t, int32(1), countedBegins(t, t.TempDir(), file), "the check of the version and the schema share one transaction, so no writer runs between them")
}

func TestPrepareSQLite_BeginsNoTransactionOnAnUpToDateFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := SQLiteFile{Name: "one.db", Schema: `CREATE TABLE IF NOT EXISTS t (x INTEGER); PRAGMA user_version = 1;`, Version: 1, Owned: []string{"t"}, BusyMs: 1000}

	require.Equal(t, int32(1), countedBegins(t, dir, file), "the first preparation makes the table")
	assert.Zero(t, countedBegins(t, dir, file), "a file at the version of the build with every table is only read")
}
