package model

import (
	"context"
	"errors"
	"time"
)

// ClaimRow is one claim: Holder holds Item. LeaseEnds is when the lease of bd on the row ends, or the zero time when bd holds no lease for it.
type ClaimRow struct {
	Item      string
	Holder    string
	LeaseEnds time.Time
}

// ErrRowMoved is returned by ClaimRows.Free when the row no longer belongs to the holder the caller read.
var ErrRowMoved = errors.New("the claim row changed hands")

// ErrNoClaim is returned by ClaimRows.Beat when the holder holds no claim row. The lease ended and another take reclaimed the row, or the holder never took one.
var ErrNoClaim = errors.New("the holder holds no claim")

// ClaimRows stores the claim rows of the project. The claim store keeps one row for each item and grants the lease, which bd renews on Beat.
// Only beads fills it, whatever tracker the project uses, because beads has the atomic operations and the leases.
//
// A method that cannot read or write its backend returns an error and never a guess: a failed read is no answer.
// TryHold sets the holder only when no holder is set. It returns nil when it lost, so a caller reads HeldRows to learn the winner.
// Reclaim frees the row only when the lease of its holder ended, and reports whether it did.
// Free changes the row only while row.Holder holds it. A row of another holder stays as it is, and the call returns ErrRowMoved. Free on a missing row is not an error.
type ClaimRows interface {
	HeldRows(ctx context.Context) ([]ClaimRow, error)
	TryHold(ctx context.Context, row ClaimRow) error
	Beat(ctx context.Context, holder string) error
	Reclaim(ctx context.Context, item string) (bool, error)
	Free(ctx context.Context, row ClaimRow) error
}
