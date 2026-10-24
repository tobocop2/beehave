package claim

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

// Rows is what claim needs from the lease store. model.ClaimRows documents each method.
type Rows interface {
	HeldRows(ctx context.Context) ([]model.ClaimRow, error)
	LastSeen(ctx context.Context) (map[string]time.Time, error)
	Beat(ctx context.Context, holder string) error
	Row(ctx context.Context, item string) (model.RowState, error)
	TryHold(ctx context.Context, row model.ClaimRow) error
	StoreLease(ctx context.Context, row model.ClaimRow) error
	Free(ctx context.Context, row model.ClaimRow) error
}

// fail turns an error of the tracker into a refusal. A refusal passes through.
func fail(err error) error {
	if err == nil {
		return nil
	}

	var refusal *Error
	if errors.As(err, &refusal) {
		return err
	}

	return &Error{Code: CodeRefused, Message: err.Error(), Err: err}
}

func (c *Claims) held(ctx context.Context) ([]model.ClaimRow, error) {
	rows, err := c.rows.HeldRows(ctx)

	return rows, fail(err)
}

func (c *Claims) state(ctx context.Context, item string) (model.RowState, error) {
	state, err := c.rows.Row(ctx, item)

	return state, fail(err)
}

// holders returns the sorted owners that hold the item.
func (c *Claims) holders(ctx context.Context, item string) ([]string, error) {
	rows, err := c.held(ctx)
	if err != nil {
		return nil, err
	}

	return holdersOf(rows, item), nil
}

func holdersOf(rows []model.ClaimRow, item string) []string {
	var names []string

	for _, r := range rows {
		if r.Item == item {
			names = append(names, r.Holder)
		}
	}

	return uniqueSorted(names)
}

func uniqueSorted(names []string) []string {
	seen := map[string]bool{}
	unique := []string{}

	for _, name := range names {
		if !seen[name] {
			seen[name] = true
			unique = append(unique, name)
		}
	}

	slices.Sort(unique)

	return unique
}

// leasesOf returns the lease each holder stored in its claim row for the item.
func leasesOf(rows []model.ClaimRow, item string) map[string]int64 {
	leases := map[string]int64{}

	for _, r := range rows {
		if r.Item == item && r.Lease != model.LeaseNone {
			leases[r.Holder] = r.Lease
		}
	}

	return leases
}

// lastSeen returns the seconds since the last heartbeat of every owner that has one.
func (c *Claims) lastSeen(ctx context.Context) (map[string]float64, error) {
	stamps, err := c.rows.LastSeen(ctx)
	if err != nil {
		return nil, fail(err)
	}

	now := c.clock()
	seen := make(map[string]float64, len(stamps))

	for holder, stamp := range stamps {
		seen[holder] = now.Sub(stamp).Seconds()
	}

	return seen, nil
}

// expired reports whether the holder was last seen longer ago than its lease. An unreadable lease never expires.
func (c *Claims) expired(holder string, seen map[string]float64, leases map[string]int64) bool {
	lease, found := leases[holder]
	if !found {
		lease = c.lease
	}

	return lease != model.LeaseUnreadable && seen[holder] > float64(lease)
}

// Who returns one line: who holds the item, if anyone.
func (c *Claims) Who(ctx context.Context, item string) (string, error) {
	if _, err := ItemOf(item); err != nil {
		return "", err
	}

	rows, err := c.held(ctx)
	if err != nil {
		return "", err
	}

	names := holdersOf(rows, item)

	switch len(names) {
	case 0:
		return fmt.Sprintf("#%s unowned", item), nil
	case 1:
		return c.whoHolds(ctx, item, names[0], leasesOf(rows, item))
	default:
		return fmt.Sprintf("#%s held by %s (MORE THAN ONE HOLDER: resolve by hand)", item, strings.Join(names, ", ")), nil
	}
}

func (c *Claims) whoHolds(ctx context.Context, item, holder string, leases map[string]int64) (string, error) {
	seen, err := c.lastSeen(ctx)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("#%s held by %s", item, c.label(holder, seen, leases)), nil
}

func (c *Claims) label(holder string, seen map[string]float64, leases map[string]int64) string {
	if leases[holder] == model.LeaseUnreadable {
		return holder + " (lease unreadable: clear the row by hand)"
	}

	if !c.expired(holder, seen, leases) {
		return holder
	}

	return fmt.Sprintf("%s (LEASE EXPIRED: last seen %s ago; `take` reclaims it)", holder, Age(seen[holder]))
}
