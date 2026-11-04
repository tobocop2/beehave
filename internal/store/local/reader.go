package local

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/tobocop2/obee/internal/store"
)

var _ store.Reader = (*Store)(nil)

// Comments returns the comments of the item in the order they were added. The same key again changed nothing, so each key shows once.
// An id that the project does not have is a refusal.
func (s *Store) Comments(ctx context.Context, id string) ([]string, error) {
	if err := s.exists(ctx, id); err != nil {
		return nil, err
	}

	got, err := list(ctx, s.db, "the comments of "+id, func(rows *sql.Rows) (string, error) {
		var text string

		return text, rows.Scan(&text)
	}, `SELECT text FROM comments WHERE project = ? AND item = ? ORDER BY rowid`, s.project, id)

	return got, err
}

// Parent returns the parent that Create recorded for the item, or an empty text when the item has none.
// An id that the project does not have is a refusal.
func (s *Store) Parent(ctx context.Context, id string) (string, error) {
	var parent sql.NullString

	err := s.db.QueryRowContext(ctx, `SELECT l.parent FROM items i LEFT JOIN item_links l ON l.project = i.project AND l.id = i.id WHERE i.project = ? AND i.id = ?`,
		s.project, id).Scan(&parent)
	if errors.Is(err, sql.ErrNoRows) {
		return "", missing(id, s.project)
	}

	if err != nil {
		return "", fmt.Errorf("local store: cannot read the parent of %s: %w", id, err)
	}

	return parent.String, nil
}

// exists refuses an id that the project does not have.
func (s *Store) exists(ctx context.Context, id string) error {
	var one int

	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM items WHERE project = ? AND id = ?`, s.project, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return missing(id, s.project)
	}

	if err != nil {
		return fmt.Errorf("local store: cannot read item %s: %w", id, err)
	}

	return nil
}
