package claim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/beehave/internal/model"
)

const attempts = 4

// Options holds what New needs. Rows is the claim store of the project. Clock tells whether the lease of a holder has ended.
type Options struct {
	Rows  Rows
	Clock func() time.Time
	Out   io.Writer
}

// Claims is the claim rows of the claim store.
type Claims struct {
	rows  Rows
	clock func() time.Time
	out   io.Writer
}

// New returns the claims of the claim store. It refuses every missing dependency.
func New(options Options) (*Claims, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}

	return &Claims{rows: options.Rows, clock: options.Clock, out: options.Out}, nil
}

func (o Options) validate() error {
	missing := map[string]bool{"claim rows": o.Rows == nil, "clock": o.Clock == nil, "output writer": o.Out == nil}

	for _, name := range slices.Sorted(maps.Keys(missing)) {
		if missing[name] {
			return refuse("claim: the %s is required", name)
		}
	}

	return nil
}

func say(w io.Writer, line string) {
	// A failed write to the output has no recovery.
	_, _ = fmt.Fprintln(w, line)
}

// Beat renews the lease of the owner. It returns a problem text when the owner holds no claim row: the claim is gone.
// Any other failure is an error, because the lease can still be live.
func (c *Claims) Beat(ctx context.Context, owner string) (string, error) {
	holder, err := OwnerOf(owner)
	if err != nil {
		return "", err
	}

	err = c.rows.Beat(ctx, holder)

	switch {
	case errors.Is(err, model.ErrNoClaim):
		return fmt.Sprintf("heartbeat for %s failed: %v", holder, err), nil
	case err != nil:
		return "", fmt.Errorf("heartbeat for %s failed: %w", holder, err)
	}

	return "", nil
}

// Renewed returns when the lease of the claim of the owner on the item was last renewed: the end of the lease, less the time a take or a renewal grants.
// It returns the zero time when the owner holds no claim on the item, or the claim has no lease. The row of another holder gives the zero time.
func (c *Claims) Renewed(ctx context.Context, owner, item string) (time.Time, error) {
	holder, err := checked(owner, item)
	if err != nil {
		return time.Time{}, err
	}

	rows, err := c.held(ctx)
	if err != nil {
		return time.Time{}, err
	}

	for _, row := range rows {
		if row.Item == item && row.Holder == holder && !row.LeaseEnds.IsZero() {
			return row.LeaseEnds.Add(-model.ClaimLease), nil
		}
	}

	return time.Time{}, nil
}

// checked validates the owner token and the item and returns the holder name.
func checked(owner, item string) (string, error) {
	me, err := OwnerOf(owner)
	if err != nil {
		return "", err
	}

	_, err = ItemOf(item)

	return me, err
}

// Release gives the item back. Another owner's hold is a refusal with code 3.
func (c *Claims) Release(ctx context.Context, owner, item string) error {
	me, err := checked(owner, item)
	if err != nil {
		return err
	}

	holders, err := c.holders(ctx, item)
	if err != nil {
		return err
	}

	if !slices.Contains(holders, me) {
		return c.releaseNothing(item, me, holders)
	}

	return c.free(ctx, me, item)
}

func (c *Claims) releaseNothing(item, me string, others []string) error {
	if len(others) > 0 {
		return held("REFUSED: #%s is held by %s, not %s", item, strings.Join(others, ", "), me)
	}

	say(c.out, fmt.Sprintf("#%s was not locked", item))

	return nil
}

// free clears the row of the owner. A row that changed hands since the read is a refusal with code 3.
func (c *Claims) free(ctx context.Context, me, item string) error {
	err := c.rows.Free(ctx, model.ClaimRow{Item: item, Holder: me})
	if errors.Is(err, model.ErrRowMoved) {
		return held("REFUSED: #%s was claimed by another owner in the same window. Do not touch it.", item)
	}

	if err = fail(err); err != nil {
		return err
	}

	say(c.out, "released #"+item)

	return nil
}

// Take claims the item for the owner. Another live holder is a refusal with code 3.
func (c *Claims) Take(ctx context.Context, owner, item string) error {
	me, err := checked(owner, item)
	if err != nil {
		return err
	}

	holders, err := c.holders(ctx, item)
	if err != nil {
		return err
	}

	if len(holders) == 1 && holders[0] == me {
		say(c.out, "#"+item+" already held by you")

		return nil
	}

	return c.claimRow(ctx, me, item)
}

// claimRow takes the row. When another holder wins, it asks the claim store to reclaim the row once, which frees it only if the lease of that holder ended, and takes again.
func (c *Claims) claimRow(ctx context.Context, me, item string) error {
	reclaimed := false

	for range attempts {
		won, holders, err := c.tryHold(ctx, me, item)
		if err != nil || won {
			return err
		}

		if len(holders) > 0 {
			if err = c.yield(ctx, item, holders, reclaimed); err != nil {
				return err
			}

			reclaimed = true
		}
	}

	return refuse("claim: could not take #%s after %d attempts; check `beehave claim who %s`", item, attempts, item)
}

// yield deals with a holder that won: after one reclaim it refuses with code 3, and before it asks for the reclaim.
func (c *Claims) yield(ctx context.Context, item string, holders []string, reclaimed bool) error {
	if reclaimed {
		return held("REFUSED: #%s is held by %s. Do not touch it.", item, strings.Join(holders, ", "))
	}

	return c.reclaim(ctx, item, holders)
}

// tryHold claims the row and reads the holders. It says TAKEN and reports true when the owner is among them.
func (c *Claims) tryHold(ctx context.Context, me, item string) (bool, []string, error) {
	if err := c.rows.TryHold(ctx, model.ClaimRow{Item: item, Holder: me}); err != nil {
		return false, nil, fail(err)
	}

	holders, err := c.holders(ctx, item)
	if err != nil || !slices.Contains(holders, me) {
		return false, holders, err
	}

	say(c.out, fmt.Sprintf("TAKEN #%s by %s", item, me))

	return true, holders, nil
}

func (c *Claims) reclaim(ctx context.Context, item string, holders []string) error {
	freed, err := c.rows.Reclaim(ctx, item)
	if err != nil {
		return fail(err)
	}

	if freed {
		say(c.out, fmt.Sprintf("reclaiming #%s from %s: its lease ended", item, strings.Join(holders, ", ")))
	}

	return nil
}

// EnsureFree refuses when another owner holds the item with a live lease. It writes nothing.
func (c *Claims) EnsureFree(ctx context.Context, owner, item string) error {
	me, err := checked(owner, item)
	if err != nil {
		return err
	}

	rows, err := c.held(ctx)
	if err != nil {
		return err
	}

	live := c.liveHolders(rows, me, item)
	if len(live) > 0 {
		return held("REFUSED: #%s is held by %s. Do not touch it.", item, strings.Join(live, ", "))
	}

	return nil
}
