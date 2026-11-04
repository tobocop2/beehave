package claim_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/claim"
	"github.com/tobocop2/beehave/internal/model"
)

const (
	leaseSeconds = 300
	nowUnix      = 1_900_000_000
	sessionA     = "session-A"
	sessionB     = "session-B"
	sessionC     = "session-C"
)

var errBoom = errors.New("boom")

// fakeRows keeps one claim row for each item in memory. It follows the store.ClaimRows rules: TryHold grants the lease and
// sets the holder only when none is set, Reclaim frees a row whose lease ended, and Free removes the row of the holder.
type fakeRows struct {
	rows   map[string]model.ClaimRow
	extra  []model.ClaimRow
	fail   map[string]error
	deaf   int
	calls  map[string]int
	onCall func(method string, call int)
}

func newFakeRows() *fakeRows {
	return &fakeRows{rows: map[string]model.ClaimRow{}, fail: map[string]error{}, calls: map[string]int{}}
}

// enter counts a call, runs the hook and returns the planned failure.
func (f *fakeRows) enter(method string) error {
	f.calls[method]++

	if f.onCall != nil {
		f.onCall(method, f.calls[method])
	}

	return f.fail[method]
}

func (f *fakeRows) HeldRows(context.Context) ([]model.ClaimRow, error) {
	if err := f.enter("HeldRows"); err != nil {
		return nil, err
	}

	held := slices.Clone(f.extra)
	for _, row := range f.rows {
		held = append(held, row)
	}

	return held, nil
}

func (f *fakeRows) Beat(_ context.Context, holder string) error {
	if err := f.enter("Beat"); err != nil {
		return err
	}

	for item, row := range f.rows {
		if row.Holder == holder {
			row.LeaseEnds = time.Unix(nowUnix+leaseSeconds, 0)
			f.rows[item] = row

			return nil
		}
	}

	return model.ErrNoClaim
}

func (f *fakeRows) TryHold(_ context.Context, row model.ClaimRow) error {
	if err := f.enter("TryHold"); err != nil {
		return err
	}

	if f.deaf > 0 {
		f.deaf--

		return nil
	}

	if _, held := f.rows[row.Item]; !held {
		f.rows[row.Item] = model.ClaimRow{Item: row.Item, Holder: row.Holder, LeaseEnds: time.Unix(nowUnix+leaseSeconds, 0)}
	}

	return nil
}

func (f *fakeRows) Reclaim(_ context.Context, item string) (bool, error) {
	if err := f.enter("Reclaim"); err != nil {
		return false, err
	}

	row, found := f.rows[item]
	if !found || row.LeaseEnds.IsZero() || !row.LeaseEnds.Before(time.Unix(nowUnix, 0)) {
		return false, nil
	}

	delete(f.rows, item)

	return true, nil
}

func (f *fakeRows) Free(_ context.Context, row model.ClaimRow) error {
	if err := f.enter("Free"); err != nil {
		return err
	}

	if current, found := f.rows[row.Item]; found && current.Holder != row.Holder {
		return model.ErrRowMoved
	}

	delete(f.rows, row.Item)

	return nil
}

type harness struct {
	t      *testing.T
	rows   *fakeRows
	out    bytes.Buffer
	claims *claim.Claims
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{t: t, rows: newFakeRows()}
	claims, err := claim.New(claim.Options{Rows: h.rows, Clock: func() time.Time { return time.Unix(nowUnix, 0) }, Out: &h.out})
	require.NoError(t, err)

	h.claims = claims

	return h
}

func (h *harness) said() string {
	return h.out.String()
}

// hold makes who the holder of item with a lease that ends secs seconds after now. A negative value is a lease that ended.
func (h *harness) hold(item, who string, secs int64) {
	h.t.Helper()

	h.rows.rows[item] = model.ClaimRow{Item: item, Holder: who, LeaseEnds: time.Unix(nowUnix+secs, 0)}
}

// holdBare makes who the holder of item with no lease, as a row set in_progress by hand.
func (h *harness) holdBare(item, who string) {
	h.t.Helper()

	h.rows.rows[item] = model.ClaimRow{Item: item, Holder: who}
}

func (h *harness) code(err error) int {
	h.t.Helper()

	var failure *claim.Error
	require.ErrorAs(h.t, err, &failure, fmt.Sprint(err))

	return failure.Code
}
