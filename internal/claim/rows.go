package claim

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tobocop2/beehave/internal/model"
)

// Rows is what claim needs from the claim store. store.ClaimRows documents each method.
type Rows interface {
	HeldRows(ctx context.Context) ([]model.ClaimRow, error)
	TryHold(ctx context.Context, row model.ClaimRow) error
	Beat(ctx context.Context, holder string) error
	Reclaim(ctx context.Context, item string) (bool, error)
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

// live reports whether the lease of the row has not ended. A row with no lease never ends: bd reclaims only a row that has a lease.
func (c *Claims) live(row model.ClaimRow) bool {
	return row.LeaseEnds.IsZero() || row.LeaseEnds.After(c.clock())
}

// liveHolders returns the sorted holders of the item other than me whose lease has not ended.
func (c *Claims) liveHolders(rows []model.ClaimRow, me, item string) []string {
	var names []string

	for _, r := range rows {
		if r.Item == item && r.Holder != me && c.live(r) {
			names = append(names, r.Holder)
		}
	}

	return uniqueSorted(names)
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
		return fmt.Sprintf("#%s held by %s", item, c.label(rows, item, names[0])), nil
	default:
		return fmt.Sprintf("#%s held by %s (MORE THAN ONE HOLDER: resolve by hand)", item, strings.Join(names, ", ")), nil
	}
}

// label names the holder, and says when its lease ended or that it has none.
func (c *Claims) label(rows []model.ClaimRow, item, holder string) string {
	i := slices.IndexFunc(rows, func(r model.ClaimRow) bool { return r.Item == item && r.Holder == holder })
	row := rows[i]

	switch {
	case row.LeaseEnds.IsZero():
		return holder + " (no lease: release it by hand)"
	case !c.live(row):
		return fmt.Sprintf("%s (LEASE ENDED %s ago; `take` reclaims it)", holder, age(c.clock().Sub(row.LeaseEnds)))
	}

	return holder
}
