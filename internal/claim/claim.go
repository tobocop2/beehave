package claim

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/tobocop2/obee/internal/model"
)

const attempts = 4

// Options holds what New needs. Rows is the lease store of the project. Nonce returns the token of one take; it never repeats.
type Options struct {
	Rows   Rows
	Lease  time.Duration
	Settle time.Duration
	Clock  func() time.Time
	Sleep  func(time.Duration)
	Nonce  func() string
	Out    io.Writer
	Warn   io.Writer
}

// RandomNonce returns a new random text. A caller passes it as Options.Nonce.
func RandomNonce() string {
	return rand.Text()
}

// Claims is the claim rows of the lease store.
type Claims struct {
	rows   Rows
	lease  int64
	settle time.Duration
	clock  func() time.Time
	sleep  func(time.Duration)
	nonce  func() string
	out    io.Writer
	warn   io.Writer
}

// New returns the claims of the lease store. It refuses a lease under one second and every missing dependency.
// A caller that has no setting takes model.DefaultClaimLease and model.DefaultClaimSettle.
func New(options Options) (*Claims, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}

	return &Claims{
		rows: options.Rows, lease: int64(options.Lease / time.Second), settle: options.Settle,
		clock: options.Clock, sleep: options.Sleep, nonce: options.Nonce, out: options.Out, warn: options.Warn,
	}, nil
}

func (o Options) validate() error {
	missing := map[string]bool{
		"claim rows": o.Rows == nil, "clock": o.Clock == nil, "sleep function": o.Sleep == nil,
		"nonce source": o.Nonce == nil, "output writer": o.Out == nil, "warning writer": o.Warn == nil,
	}

	for _, name := range slices.Sorted(maps.Keys(missing)) {
		if missing[name] {
			return refuse("claim: the %s is required", name)
		}
	}

	if o.Lease < model.MinClaimLease {
		return refuse("claim: the lease must be at least %s, got %s; a shorter lease lets a second run take a live hold", model.MinClaimLease, o.Lease)
	}

	if o.Settle < 0 {
		return refuse("claim: the settle pause must not be negative, got %s", o.Settle)
	}

	return nil
}

func say(w io.Writer, line string) {
	// A failed write to the output has no recovery.
	_, _ = fmt.Fprintln(w, line)
}

// Beat stamps the heartbeat of the owner. It returns a problem text, or "" when all went well.
func (c *Claims) Beat(ctx context.Context, owner string) (string, error) {
	holder, err := OwnerOf(owner)
	if err != nil {
		return "", err
	}

	return c.beat(ctx, holder), nil
}

// beat stamps the heartbeat of the holder. It returns a problem text, or "" when all went well.
func (c *Claims) beat(ctx context.Context, holder string) string {
	if err := c.rows.Beat(ctx, holder); err != nil {
		return fmt.Sprintf("heartbeat for %s failed: %v", holder, err)
	}

	return ""
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

// free clears the row of the owner and reads it back: a release that did not take effect is a refusal.
// The row is cleared only while it is the take that was read, so a take made since stays.
func (c *Claims) free(ctx context.Context, me, item string) error {
	state, err := c.state(ctx, item)
	if err != nil {
		return err
	}

	if !state.Held || state.Holder != me {
		return c.releaseNothing(item, me, heldBy(state))
	}

	if err = c.freeRow(ctx, item, state); err != nil {
		return err
	}

	return c.confirmFree(ctx, item)
}

// confirmFree reads the row back after a free.
func (c *Claims) confirmFree(ctx context.Context, item string) error {
	state, err := c.state(ctx, item)
	if err != nil {
		return err
	}

	if state.Held {
		return refuse("claim: release of #%s did not take effect: still held by %q", item, state.Holder)
	}

	say(c.out, "released #"+item)

	return nil
}

func heldBy(state model.RowState) []string {
	if state.Held {
		return []string{state.Holder}
	}

	return nil
}

// freeRow clears the row described by state. A row that changed hands since the read is a refusal with code 3.
func (c *Claims) freeRow(ctx context.Context, item string, state model.RowState) error {
	err := c.rows.Free(ctx, model.ClaimRow{Item: item, Holder: state.Holder, Nonce: state.Nonce})
	if errors.Is(err, model.ErrRowMoved) {
		return held("REFUSED: #%s was claimed by another owner in the same window. Do not touch it.", item)
	}

	return fail(err)
}

// Take claims the item for the owner. Another live holder is a refusal with code 3.
func (c *Claims) Take(ctx context.Context, owner, item string) error {
	me, err := checked(owner, item)
	if err != nil {
		return err
	}

	c.beatOrWarn(ctx, me)

	if done, err := c.prepare(ctx, me, item); done || err != nil {
		return err
	}

	return c.claimRow(ctx, me, item)
}

// prepare refuses a live holder and reopens expired rows. It reports true when the owner holds the item already.
func (c *Claims) prepare(ctx context.Context, me, item string) (bool, error) {
	holders, err := c.holders(ctx, item)
	if err != nil {
		return false, err
	}

	if len(holders) == 1 && holders[0] == me {
		say(c.out, "#"+item+" already held by you")

		return true, nil
	}

	return false, c.clearExpired(ctx, me, item, holders)
}

func (c *Claims) beatOrWarn(ctx context.Context, holder string) {
	if problem := c.beat(ctx, holder); problem != "" {
		say(c.warn, fmt.Sprintf("WARNING: %s. Other runs will read this owner's claims as expired after %s.", problem, Age(float64(c.lease))))
	}
}

// EnsureFree refuses when another owner holds the item. It writes nothing.
func (c *Claims) EnsureFree(ctx context.Context, owner, item string) error {
	me, err := checked(owner, item)
	if err != nil {
		return err
	}

	holders, err := c.holders(ctx, item)
	if err != nil {
		return err
	}

	_, _, err = c.refuseLive(ctx, me, item, holders)

	return err
}

// refuseLive refuses a live holder. It returns the other holders and when each was last seen.
func (c *Claims) refuseLive(ctx context.Context, me, item string, holders []string) ([]string, map[string]float64, error) {
	others := slices.DeleteFunc(slices.Clone(holders), func(who string) bool { return who == me })
	if len(others) == 0 {
		return nil, nil, nil
	}

	seen, live, err := c.liveHolders(ctx, item, others)
	if err != nil {
		return nil, nil, err
	}

	if len(live) > 0 {
		return nil, nil, held("REFUSED: #%s is held by %s. Do not touch it.", item, strings.Join(live, ", "))
	}

	return others, seen, nil
}

// liveHolders returns when each owner was last seen and the owners among others whose lease has not run out.
func (c *Claims) liveHolders(ctx context.Context, item string, others []string) (map[string]float64, []string, error) {
	seen, err := c.lastSeen(ctx)
	if err != nil {
		return nil, nil, err
	}

	rows, err := c.held(ctx)
	if err != nil {
		return nil, nil, err
	}

	leases := leasesOf(rows, item)

	return seen, slices.DeleteFunc(slices.Clone(others), func(who string) bool { return c.expired(who, seen, leases) }), nil
}

// clearExpired refuses a live holder and frees the row when its holder's lease ran out.
func (c *Claims) clearExpired(ctx context.Context, me, item string, holders []string) error {
	others, seen, err := c.refuseLive(ctx, me, item, holders)
	if err != nil || len(others) == 0 {
		return err
	}

	rows, err := c.held(ctx)
	if err != nil {
		return err
	}

	return c.reopen(ctx, item, others, seen, leasesOf(rows, item))
}

// reopen frees the row only when the expired holder still holds it: a row claimed since the read is not expired.
func (c *Claims) reopen(ctx context.Context, item string, others []string, seen map[string]float64, leases map[string]int64) error {
	state, err := c.state(ctx, item)
	if err != nil || !state.Held || !slices.Contains(others, state.Holder) {
		return err
	}

	if err = c.freeRow(ctx, item, state); err != nil {
		return err
	}

	lease, found := leases[state.Holder]
	if !found {
		lease = c.lease
	}

	say(c.out, fmt.Sprintf("reclaiming #%s from %s: last seen %s ago, lease %s", item, state.Holder, Age(seen[state.Holder]), Age(float64(lease))))

	return nil
}

func (c *Claims) claimRow(ctx context.Context, me, item string) error {
	row := model.ClaimRow{Item: item, Holder: me, Lease: c.lease, Nonce: c.nonce()}

	for range attempts {
		done, err := c.tryOnce(ctx, row)
		if done || err != nil {
			return err
		}
	}

	return refuse("claim: could not settle ownership of #%s after %d attempts; check `obee claim who %s`", item, attempts, item)
}

// tryOnce makes one attempt to hold the row. It reports true when the owner holds the row.
// A row that is unassigned after the attempt was reset by a concurrent write, so the caller tries again.
func (c *Claims) tryOnce(ctx context.Context, row model.ClaimRow) (bool, error) {
	if err := c.rows.TryHold(ctx, row); err != nil {
		return false, fail(err)
	}

	settled, state, err := c.settleRow(ctx, row)
	if err != nil {
		return false, err
	}

	if settled {
		return true, c.finishTake(ctx, row)
	}

	if state.Holder != "" && state.Holder != row.Holder {
		return false, held("REFUSED: #%s was claimed by %s in the same window.", row.Item, state.Holder)
	}

	return false, nil
}

// settleRow reads the row back, waits, and reads it again. It reports whether the row is still ours.
func (c *Claims) settleRow(ctx context.Context, row model.ClaimRow) (bool, model.RowState, error) {
	state, err := c.state(ctx, row.Item)
	if err != nil || !isMine(state, row) {
		return false, state, err
	}

	c.sleep(c.settle)

	state, err = c.state(ctx, row.Item)

	return err == nil && isMine(state, row), state, err
}

// isMine reports whether the row state is the take of row. bd 1.3.2 stores the token of every take in the assignee,
// so a row with no token, or another token, is not the take of row.
func isMine(state model.RowState, row model.ClaimRow) bool {
	return state.Held && state.Holder == row.Holder && state.Nonce == row.Nonce
}

// finishTake stores the lease in the row the owner holds, then reads the row back: a take that another owner replaced is a refusal.
func (c *Claims) finishTake(ctx context.Context, row model.ClaimRow) error {
	if err := c.rows.StoreLease(ctx, row); err != nil {
		return c.lostOrFailed(err, row)
	}

	state, err := c.state(ctx, row.Item)
	if err != nil {
		return err
	}

	if !isMine(state, row) {
		return lostWindow(row)
	}

	say(c.out, fmt.Sprintf("TAKEN #%s by %s", row.Item, row.Holder))

	return nil
}

func (c *Claims) lostOrFailed(err error, row model.ClaimRow) error {
	if errors.Is(err, model.ErrRowMoved) {
		return lostWindow(row)
	}

	return fail(err)
}

func lostWindow(row model.ClaimRow) error {
	return held("REFUSED: #%s was claimed by another owner in the same window.", row.Item)
}
