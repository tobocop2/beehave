package model

import (
	"errors"
	"time"
)

// ClaimRow is one claim: Holder holds Item. LeaseEnds is when the lease of bd on the row ends, or the zero time when bd holds no lease for it.
type ClaimRow struct {
	Item      string
	Holder    string
	LeaseEnds time.Time
}

// ClaimLease is the time a take or a renewal holds a claim row. A store grants it, and the engine trusts a claim for it, less one tick.
const ClaimLease = 5 * time.Minute

// ErrRowMoved is returned by ClaimRows.Free when the row no longer belongs to the holder the caller read.
var ErrRowMoved = errors.New("the claim row changed hands")

// ErrNoClaim is returned by ClaimRows.Beat when the holder holds no claim row. The lease ended and another take reclaimed the row, or the holder never took one.
var ErrNoClaim = errors.New("the holder holds no claim")
