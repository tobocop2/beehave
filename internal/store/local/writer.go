package local

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/tobocop2/obee/internal/store"
)

var _ store.Writer = (*Store)(nil)

// writerSchema adds the tables of the writer to a file. Each statement may run again on a file that has them, and a file of an older build gets the tables on its next open.
// The tables are additive: an older build ignores them, so the schema version stays 1.
// item_keys maps a key to the item that Create filed for it, so a repeated Create finds that item. comments holds one row for each item and key.
const writerSchema = `
CREATE TABLE IF NOT EXISTS item_keys (
	project TEXT NOT NULL,
	key     TEXT NOT NULL,
	id      TEXT NOT NULL,
	PRIMARY KEY (project, key)
);
CREATE TABLE IF NOT EXISTS item_links (
	project TEXT NOT NULL,
	id      TEXT NOT NULL,
	parent  TEXT NOT NULL,
	PRIMARY KEY (project, id)
);
CREATE TABLE IF NOT EXISTS comments (
	project TEXT NOT NULL,
	item    TEXT NOT NULL,
	key     TEXT NOT NULL,
	text    TEXT NOT NULL,
	PRIMARY KEY (project, item, key)
);
`

// Create files an open item for the key and returns its id. The ids are the project name, a hyphen and a number that no item of the project uses.
// A key that has an item returns that item and created = false. The lookup and the insert run in one BEGIN IMMEDIATE transaction, so two writers on the file file one item.
func (s *Store) Create(ctx context.Context, key string, item store.NewItem) (string, bool, error) {
	if err := refuseItem(key, item); err != nil {
		return "", false, err
	}

	var (
		id      string
		created bool
	)

	err := inTx(ctx, s.db, "create for "+key, func(tx *sql.Tx) (err error) {
		id, created, err = s.findOrInsert(ctx, tx, key, item)

		return err
	})

	return id, created, err
}

// refuseItem returns the refusal of an item that Create cannot file: no key, no title, or an assignee, which the local store does not keep.
func refuseItem(key string, item store.NewItem) error {
	switch {
	case key == "":
		return store.Refuse("an item write needs a key")
	case item.Title == "":
		return store.Refuse("an item needs a title")
	case item.Assignee != "":
		return errNoAssignee
	}

	return nil
}

// findOrInsert returns the item of the key, or files the item when the key has none.
func (s *Store) findOrInsert(ctx context.Context, tx *sql.Tx, key string, item store.NewItem) (string, bool, error) {
	found, err := keyed(ctx, tx, s.project, key)
	if err != nil || found != "" {
		return found, false, err
	}

	id, err := s.insert(ctx, tx, key, item)

	return id, err == nil, err
}

// keyed returns the id that the key has, or empty when it has none.
func keyed(ctx context.Context, tx *sql.Tx, project, key string) (string, error) {
	var id string

	err := tx.QueryRowContext(ctx, `SELECT id FROM item_keys WHERE project = ? AND key = ?`, project, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}

	return id, err
}

// insert adds the item, its key and its parent link in the transaction.
func (s *Store) insert(ctx context.Context, tx *sql.Tx, key string, item store.NewItem) (string, error) {
	id, err := s.addNumbered(ctx, tx, item)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO item_keys (project, key, id) VALUES (?, ?, ?)`, s.project, key, id)
	}

	if err == nil && item.Parent != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO item_links (project, id, parent) VALUES (?, ?, ?)`, s.project, id, item.Parent)
	}

	return id, err
}

// addNumbered files the item under the first id of the form project-N that the project does not use, counting from the number of items plus one.
// An insert that finds the id taken adds no row, and the next number is tried.
func (s *Store) addNumbered(ctx context.Context, tx *sql.Tx, item store.NewItem) (string, error) {
	var count int

	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM items WHERE project = ?`, s.project).Scan(&count)

	var id string

	for number := count + 1; err == nil && id == ""; number++ {
		candidate := fmt.Sprintf("%s-%d", s.project, number)

		var one int

		switch err = tx.QueryRowContext(ctx, insertItem, s.project, candidate, item.Title, item.Body, stateOpen).Scan(&one); {
		case err == nil:
			id = candidate
		case errors.Is(err, sql.ErrNoRows):
			err = nil
		}
	}

	return id, err
}

// SetStatus sets the state of the item. The same state again changes nothing. An id that the project does not have is a refusal.
func (s *Store) SetStatus(ctx context.Context, id string, status store.Status) error {
	var one int

	err := s.db.QueryRowContext(ctx, `UPDATE items SET state = ? WHERE project = ? AND id = ? RETURNING 1`, string(status), s.project, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return missing(id, s.project)
	}

	if err != nil {
		return fmt.Errorf("local store: cannot set the status of item %s: %w", id, err)
	}

	return nil
}

// errNoAssignee is the refusal of an assignee: the local store keeps none.
var errNoAssignee = store.Refuse("the local store keeps no assignee. Leave the assignee out, or use a tracker that keeps one")

// Assign refuses: the local store keeps no assignee.
func (*Store) Assign(context.Context, string, string) error { return errNoAssignee }

// Comment adds a comment to the item once for each key. The same key again changes nothing. An id that the project does not have is a refusal.
func (s *Store) Comment(ctx context.Context, id, key, text string) error {
	return inTx(ctx, s.db, "comment on "+id, func(tx *sql.Tx) error {
		var one int

		err := tx.QueryRowContext(ctx, `SELECT 1 FROM items WHERE project = ? AND id = ?`, s.project, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return missing(id, s.project)
		}

		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO comments (project, item, key, text) VALUES (?, ?, ?, ?)`, s.project, id, key, text)

		return err
	})
}

func missing(id, project string) *store.Error {
	return store.Refuse("no item %s in project %s", id, project)
}
