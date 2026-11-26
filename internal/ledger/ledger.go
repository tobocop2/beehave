// Package ledger owns ledger.db, the lasting record of the rounds and of the spend. Retention of the runs never touches it.
package ledger

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/tobocop2/beehave/internal/store"
)

const (
	// FileName is the name of the ledger file in the state folder.
	FileName = "ledger.db"

	schemaVersion = 1
	busyTimeoutMs = 30000
)

// schema creates the tables of schemaVersion. Each statement may run again on a file that has them.
// A spend row is one agent execution. call is the id of that execution, so no workflow id or step number gives a row its identity.
// A NULL cost means the cost is unknown.
const schema = `
CREATE TABLE IF NOT EXISTS rounds (
	base           TEXT NOT NULL,
	round          INTEGER NOT NULL,
	status         TEXT NOT NULL,
	last_pass      INTEGER NOT NULL,
	integration_pr INTEGER NOT NULL,
	build_version  TEXT NOT NULL,
	step_versions  TEXT NOT NULL,
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
	call         TEXT NOT NULL PRIMARY KEY,
	tree         TEXT NOT NULL,
	run_id       TEXT NOT NULL,
	step         INTEGER NOT NULL,
	session      TEXT NOT NULL,
	item         TEXT NOT NULL,
	pull_request INTEGER NOT NULL,
	call_name    TEXT NOT NULL,
	cost         REAL,
	at           INTEGER NOT NULL
);
PRAGMA user_version = 1;
`

// Ledger is the open ledger.db of one state folder.
type Ledger struct {
	db *sql.DB

	// running holds the ids of the executions that this process started and has not ended. Their cost is not unknown yet: it is still to come.
	mu      sync.Mutex
	running map[string]struct{}
}

// Open opens ledger.db in the state folder and creates the file and the tables when they are missing. The folder must exist.
// The file and its -wal and -shm files have mode 0600. A symbolic link at the path, a file of another schema version and a new file of another owner are refused.
func Open(ctx context.Context, stateDir string) (*Ledger, error) {
	db, err := store.OpenSQLite(ctx, stateDir, store.SQLiteFile{
		Name:    FileName,
		Schema:  schema,
		Version: schemaVersion,
		Owned:   []string{"rounds", "round_items", "spend"},
		BusyMs:  busyTimeoutMs,
	})
	if err != nil {
		return nil, fmt.Errorf("the ledger: %w", err)
	}

	return &Ledger{db: db, running: map[string]struct{}{}}, nil
}

// DB returns the database handle.
func (l *Ledger) DB() *sql.DB { return l.db }

// Close closes the database.
func (l *Ledger) Close() error { return l.db.Close() }
