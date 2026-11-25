package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
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

func countedBegins(t *testing.T, file SQLiteFile) int32 {
	t.Helper()

	var begins atomic.Int32

	dsn := SQLiteDSN(filepath.Join(t.TempDir(), file.Name), file.BusyMs, url.Values{"_pragma": {"journal_mode(WAL)"}, "_txlock": {"immediate"}})
	db := sql.OpenDB(countingConnector{dsn: dsn, begins: &begins})

	t.Cleanup(func() { _ = db.Close() })

	require.NoError(t, prepareSQLite(t.Context(), db, file))

	return begins.Load()
}

func TestPrepareSQLite_ReadsTheVersionAndRunsTheSchemaInOneTransaction(t *testing.T) {
	t.Parallel()

	file := SQLiteFile{Name: "one.db", Schema: `CREATE TABLE IF NOT EXISTS t (x INTEGER); PRAGMA user_version = 1;`, Version: 1, Owned: []string{"t"}, BusyMs: 1000}

	assert.Equal(t, int32(1), countedBegins(t, file), "the check of the version and the schema share one transaction, so no writer runs between them")
}
