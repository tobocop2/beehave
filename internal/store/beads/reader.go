package beads

import (
	"context"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store"
)

var _ store.Reader = (*Store)(nil)

// Comments returns the text of the comments of the item, oldest first, from bd comments. A marker line that a write added is dropped.
func (s *Store) Comments(ctx context.Context, id string) ([]string, error) {
	done := s.run.Run(ctx, []string{argComments, id, argJSON})
	if done.ExitCode != 0 {
		return nil, store.Refuse("REFUSE: cannot read the comments of item %s in beads", id)
	}

	rows, err := model.Decode[[]bdComment]("bd comments", done.Stdout)
	if err != nil {
		return nil, store.Unreadable(err)
	}

	texts := make([]string, len(rows))
	for i, row := range rows {
		texts[i] = store.Visible(row.Text)
	}

	return texts, nil
}

// Parent returns the parent field of bd show, or an empty text for an item with no parent.
func (s *Store) Parent(ctx context.Context, id string) (string, error) {
	row, err := s.shown(ctx, id)
	if err != nil || row.Parent == "" {
		return "", err
	}

	return readableID(row.Parent)
}
