// Package store holds the contracts of the issue tracker and the claim rows, with their errors. Each backend is a child package.
package store

import (
	"context"

	"github.com/tobocop2/obee/internal/model"
)

// Tracker is an issue tracker: work items and their parents. beads and GitHub Issues implement it. The claim rows are not part of it.
// ItemFields lists the names of the fields that Item returns; every tracker declares id, title and body. It needs no call to the tracker.
// Item reads one item and returns one value for each declared field. A failed read is an error.
type Tracker interface {
	ItemFields() []string
	Item(ctx context.Context, id string) (map[string]string, error)
	OpenItems(ctx context.Context) ([]model.Item, error)
	Containers(ctx context.Context) ([]string, error)
	Children(ctx context.Context, container string) ([]string, error)
	Show(ctx context.Context, item string) (string, error)
	Close(ctx context.Context, item, reason string) error
	Create(ctx context.Context, request model.CreateRequest) (string, error)
}

// ClaimRows stores the claim rows of the project. The claim store keeps one row for each item and grants the lease, which bd renews on Beat.
// Only beads fills it, whatever tracker the project uses, because beads has the atomic operations and the leases.
//
// A method that cannot read or write its backend returns an error and never a guess: a failed read is no answer.
// TryHold sets the holder only when no holder is set. It returns nil when it lost, so a caller reads HeldRows to learn the winner.
// Reclaim frees the row only when the lease of its holder ended, and reports whether it did.
// Free changes the row only while row.Holder holds it. A row of another holder stays as it is, and the call returns model.ErrRowMoved. Free on a missing row is not an error.
type ClaimRows interface {
	HeldRows(ctx context.Context) ([]model.ClaimRow, error)
	TryHold(ctx context.Context, row model.ClaimRow) error
	Beat(ctx context.Context, holder string) error
	Reclaim(ctx context.Context, item string) (bool, error)
	Free(ctx context.Context, row model.ClaimRow) error
}
