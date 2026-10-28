// Package local is the store that needs no other tool: the items and the claim rows of one project in one SQLite file, local.db, in the state folder.
// A Store belongs to one project, as a beads store belongs to one prefix. Many Store values, in one process or in many, may share the file.
// It implements store.Tracker and store.ClaimRows. The claim rows are as wide as the state folder: two machines need a shared store.
package local

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	// The driver registers itself under the name sqlite, as it does for the workflow store.
	_ "modernc.org/sqlite"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
)

const (
	databaseFile  = "local.db"
	fileMode      = 0o600
	dirMode       = 0o700
	schemaVersion = 1
	busyTimeoutMs = 5000
	stateOpen     = "open"
)

// schema creates the tables of schemaVersion. Each statement may run again on a file that has them.
const schema = `
CREATE TABLE IF NOT EXISTS items (
	project TEXT NOT NULL,
	id      TEXT NOT NULL,
	title   TEXT NOT NULL,
	body    TEXT NOT NULL,
	state   TEXT NOT NULL,
	PRIMARY KEY (project, id)
);
CREATE TABLE IF NOT EXISTS claims (
	project    TEXT NOT NULL,
	item       TEXT NOT NULL,
	holder     TEXT NOT NULL,
	lease_ends INTEGER NOT NULL,
	PRIMARY KEY (project, item)
);
PRAGMA user_version = 1;
`

// insertItem adds an open item and returns a row only when it did. An id that exists is left as it is.
const insertItem = `INSERT INTO items (project, id, title, body, state) VALUES (?, ?, ?, ?, ?) ON CONFLICT DO NOTHING RETURNING 1`

// Store is the items and the claim rows of one project.
type Store struct {
	db      *sql.DB
	project string
	now     func() time.Time
}

// Open opens local.db in stateDir for a project and creates the folder, the file and the tables when they are missing.
// The folder has mode 0700 and the file has mode 0600. SQLite gives its -wal and -shm files the mode of the file. now is the clock of the leases.
func Open(ctx context.Context, stateDir, project string, now func() time.Time) (*Store, error) {
	if err := create(stateDir); err != nil {
		return nil, err
	}

	db, err := connect(ctx, filepath.Join(stateDir, databaseFile))
	if err != nil {
		return nil, err
	}

	return &Store{db: db, project: project, now: now}, nil
}

// create makes the state folder and an empty database file in it. The file has mode 0600 before SQLite opens it, so SQLite makes its -wal and -shm files with that mode.
func create(stateDir string) error {
	if err := os.MkdirAll(stateDir, dirMode); err != nil {
		return fmt.Errorf("local store: cannot create the state folder: %w", err)
	}

	root, err := os.OpenRoot(stateDir)
	if err == nil {
		err = errors.Join(touch(root), root.Close())
	}

	if err != nil {
		return fmt.Errorf("local store: cannot create %s: %w", databaseFile, err)
	}

	return nil
}

// touch creates the database file in root when it is missing, and leaves a file that exists as it is.
func touch(root *os.Root) error {
	file, err := root.OpenFile(databaseFile, os.O_CREATE|os.O_RDWR, fileMode)
	if err == nil {
		err = file.Close()
	}

	return err
}

// connect opens the database and prepares it. A database that cannot be prepared is closed.
func connect(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err == nil {
		err = prepare(ctx, db)
		if err != nil {
			err = errors.Join(err, db.Close())
		}
	}

	return db, err
}

// dsn returns the address of the file. Each connection waits for a lock, uses a write-ahead log, and starts a transaction with BEGIN IMMEDIATE.
func dsn(path string) string {
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMs))
	query.Add("_pragma", "journal_mode(WAL)")
	query.Set("_txlock", "immediate")

	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

// prepare creates the tables on a new file and refuses a file of a schema version it does not know.
func prepare(ctx context.Context, db *sql.DB) error {
	return inTx(ctx, db, "open", func(tx *sql.Tx) error {
		var version int

		err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
		if err == nil && version != 0 && version != schemaVersion {
			err = fmt.Errorf("the file has schema version %d, and this build knows version %d", version, schemaVersion)
		}

		if err == nil {
			_, err = tx.ExecContext(ctx, schema)
		}

		return err
	})
}

// inTx runs fn in one BEGIN IMMEDIATE transaction, so no other writer runs between its statements. An error from fn rolls it back.
func inTx(ctx context.Context, db *sql.DB, what string, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("local store: %s: %w", what, err)
	}

	if err = fn(tx); err == nil {
		err = tx.Commit()
	} else {
		err = errors.Join(err, tx.Rollback())
	}

	if err != nil {
		return fmt.Errorf("local store: %s: %w", what, err)
	}

	return nil
}

// Close closes the file.
func (s *Store) Close() error { return s.db.Close() }

// ItemFields declares the fields that Item returns.
func (*Store) ItemFields() []string { return ItemFields() }

// ItemFields declares the fields that a Store returns for an item. A caller that only needs the names does not open the file.
func ItemFields() []string { return []string{"id", "title", "body"} }

// Add files an open item. It refuses an empty id, an empty title, and an id that the project already has.
func (s *Store) Add(ctx context.Context, id, title, body string) error {
	switch {
	case id == "":
		return store.Refuse("an item needs an id")
	case title == "":
		return store.Refuse("an item needs a title")
	}

	var one int

	err := s.db.QueryRowContext(ctx, insertItem, s.project, id, title, body, stateOpen).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Refuse("item %s already exists in project %s", id, s.project)
	}

	if err != nil {
		return fmt.Errorf("local store: cannot add item %s: %w", id, err)
	}

	return nil
}

// Item reads one item and returns its id, title and body. An id that the project does not have is a refusal that names the fix.
func (s *Store) Item(ctx context.Context, id string) (map[string]string, error) {
	var title, body string

	err := s.db.QueryRowContext(ctx, `SELECT title, body FROM items WHERE project = ? AND id = ?`, s.project, id).Scan(&title, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.Refuse("no item %s in project %s. Add it: obee item add", id, s.project)
	}

	if err != nil {
		return nil, fmt.Errorf("local store: cannot read item %s: %w", id, err)
	}

	return map[string]string{"id": id, "title": title, "body": body}, nil
}

// OpenItems lists the open items of the project in the order they were added. A local item has no severity.
func (s *Store) OpenItems(ctx context.Context) ([]model.Item, error) {
	return list(ctx, s.db, "the open items", func(rows *sql.Rows) (model.Item, error) {
		item := model.Item{Severity: model.SeverityUnknown}
		err := rows.Scan(&item.ID, &item.Title)

		return item, err
	}, `SELECT id, title FROM items WHERE project = ? AND state = ? ORDER BY rowid`, s.project, stateOpen)
}

// HeldRows returns the claim rows that have a holder, with the end of each lease.
func (s *Store) HeldRows(ctx context.Context) ([]model.ClaimRow, error) {
	return list(ctx, s.db, "the held rows", func(rows *sql.Rows) (model.ClaimRow, error) {
		var (
			row  model.ClaimRow
			ends int64
		)

		err := rows.Scan(&row.Item, &row.Holder, &ends)
		row.LeaseEnds = time.UnixMilli(ends).UTC()

		return row, err
	}, `SELECT item, holder, lease_ends FROM claims WHERE project = ? AND holder <> '' ORDER BY item`, s.project)
}

// list runs a query and reads each row with read.
func list[T any](ctx context.Context, db *sql.DB, what string, read func(*sql.Rows) (T, error), query string, args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("local store: cannot read %s: %w", what, err)
	}

	defer rows.Close()

	var got []T

	for rows.Next() {
		one, err := read(rows)
		if err != nil {
			return nil, fmt.Errorf("local store: cannot read %s: %w", what, err)
		}

		got = append(got, one)
	}

	return got, rows.Err()
}

// TryHold gives the row to row.Holder when the item has no holder. It returns nil when it lost: HeldRows names the winner.
// The lease ends model.ClaimLease after the take. row.LeaseEnds is not read.
func (s *Store) TryHold(ctx context.Context, row model.ClaimRow) error {
	if row.Item == "" || row.Holder == "" {
		return store.Refuse("a claim needs an item and a holder")
	}

	ends := s.now().Add(model.ClaimLease).UnixMilli()

	return inTx(ctx, s.db, "take "+row.Item, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO claims (project, item, holder, lease_ends) VALUES (?, ?, '', 0)`, s.project, row.Item); err != nil {
			return err
		}

		_, err := tx.ExecContext(ctx, `UPDATE claims SET holder = ?, lease_ends = ? WHERE project = ? AND item = ? AND holder = ''`, row.Holder, ends, s.project, row.Item)

		return err
	})
}

// Beat moves the lease of each row of the holder to model.ClaimLease from now. A holder with no row gets model.ErrNoClaim.
func (s *Store) Beat(ctx context.Context, holder string) error {
	var one int

	err := s.db.QueryRowContext(ctx, `UPDATE claims SET lease_ends = ? WHERE project = ? AND holder = ? AND holder <> '' RETURNING 1`,
		s.now().Add(model.ClaimLease).UnixMilli(), s.project, holder).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("local store: beat for %q: %w", holder, model.ErrNoClaim)
	}

	if err != nil {
		return fmt.Errorf("local store: beat for %q: %w", holder, err)
	}

	return nil
}

// Reclaim frees the row of the item when its lease has ended, and reports whether it did.
func (s *Store) Reclaim(ctx context.Context, item string) (bool, error) {
	var one int

	err := s.db.QueryRowContext(ctx, `UPDATE claims SET holder = '', lease_ends = 0 WHERE project = ? AND item = ? AND holder <> '' AND lease_ends <= ? RETURNING 1`,
		s.project, item, s.now().UnixMilli()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("local store: cannot reclaim %s: %w", item, err)
	}

	return true, nil
}

// Free clears the row only while row.Holder holds it. A row of another holder stays as it is, and the call returns model.ErrRowMoved.
// A row with no holder, or no row, is not an error.
func (s *Store) Free(ctx context.Context, row model.ClaimRow) error {
	return inTx(ctx, s.db, "free "+row.Item, func(tx *sql.Tx) error {
		var others int

		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM claims WHERE project = ? AND item = ? AND holder <> '' AND holder <> ?`, s.project, row.Item, row.Holder).Scan(&others); err != nil {
			return err
		}

		if others > 0 {
			return model.ErrRowMoved
		}

		_, err := tx.ExecContext(ctx, `UPDATE claims SET holder = '', lease_ends = 0 WHERE project = ? AND item = ? AND holder = ?`, s.project, row.Item, row.Holder)

		return err
	})
}
