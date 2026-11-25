// Package ledger owns ledger.db, the lasting record of the rounds and of the spend. Retention of the runs never touches it.
package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	// The driver registers itself under the name sqlite, as it does for the workflow store.
	_ "modernc.org/sqlite"

	"github.com/tobocop2/beehave/internal/store"
)

const (
	// FileName is the name of the ledger file in the state folder.
	FileName = "ledger.db"

	fileMode      = 0o600
	busyTimeoutMs = 30000
	schemaVersion = 1
)

// schema creates the tables of schemaVersion. Each statement may run again on a file that has them.
// A NULL cost means the cost is unknown. A spend row is one agent execution, so the run and the step make its key.
const schema = `
CREATE TABLE IF NOT EXISTS rounds (
	base          TEXT NOT NULL,
	round         INTEGER NOT NULL,
	status        TEXT NOT NULL,
	last_pass     INTEGER NOT NULL,
	integration_pr INTEGER NOT NULL,
	build_version TEXT NOT NULL,
	step_versions TEXT NOT NULL,
	PRIMARY KEY (base, round)
);
CREATE TABLE IF NOT EXISTS round_items (
	base         TEXT NOT NULL,
	round        INTEGER NOT NULL,
	item         TEXT NOT NULL,
	attempt      INTEGER NOT NULL,
	run_id       TEXT NOT NULL,
	branch       TEXT NOT NULL,
	pull_request INTEGER NOT NULL,
	head         TEXT NOT NULL,
	state        TEXT NOT NULL,
	PRIMARY KEY (base, round, item)
);
CREATE TABLE IF NOT EXISTS spend (
	tree         TEXT NOT NULL,
	run_id       TEXT NOT NULL,
	step         INTEGER NOT NULL,
	session      TEXT NOT NULL,
	item         TEXT NOT NULL,
	pull_request INTEGER NOT NULL,
	call_name    TEXT NOT NULL,
	cost         REAL,
	at           INTEGER NOT NULL,
	PRIMARY KEY (run_id, step)
);
PRAGMA user_version = 1;
`

// Ledger is the open ledger.db of one state folder.
type Ledger struct {
	db *sql.DB
}

// Open opens ledger.db in the state folder and creates the file and the tables when they are missing. The folder must exist.
// The file has mode 0600 before SQLite opens it, so SQLite gives its -wal and -shm files that mode.
func Open(ctx context.Context, stateDir string) (*Ledger, error) {
	path := filepath.Join(stateDir, FileName)

	if err := create(path); err != nil {
		return nil, fmt.Errorf("cannot create %s: %w", FileName, err)
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err == nil {
		err = prepare(ctx, db)
		if err != nil {
			err = errors.Join(err, db.Close())
		}
	}

	if err != nil {
		return nil, fmt.Errorf("open %s: %w", FileName, err)
	}

	return &Ledger{db: db}, nil
}

// create makes an empty file with mode 0600 when the file is missing, and leaves a file that exists as it is.
func create(path string) error {
	file, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return err
	}

	return file.Close()
}

// dsn returns the address of the file. Each connection waits for a lock, uses a write-ahead log, and starts a transaction with BEGIN IMMEDIATE.
func dsn(path string) string {
	return store.SQLiteDSN(path, busyTimeoutMs, url.Values{"_pragma": {"journal_mode(WAL)"}, "_txlock": {"immediate"}})
}

// prepare creates the tables on a new file and refuses a file of a schema version it does not know.
func prepare(ctx context.Context, db *sql.DB) error {
	var version int

	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}

	if version != 0 && version != schemaVersion {
		return fmt.Errorf("the file has schema version %d, and this build knows version %d", version, schemaVersion)
	}

	_, err := db.ExecContext(ctx, schema)

	return err
}

// DB returns the database handle. The workflow engine passes it to its data source, so a write and the record of its step commit together.
func (l *Ledger) DB() *sql.DB { return l.db }

// Close closes the database.
func (l *Ledger) Close() error { return l.db.Close() }
