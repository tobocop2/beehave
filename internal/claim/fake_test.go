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

	"github.com/tobocop2/obee/internal/claim"
	"github.com/tobocop2/obee/internal/model"
)

const (
	lease    = 7200
	nowUnix  = 1_900_000_000
	sessionA = "session-A"
	sessionB = "session-B"
	sessionC = "session-C"
)

var errBoom = errors.New("boom")

// fakeRows keeps one claim row for each item in memory. It follows the model.ClaimRows rules:
// TryHold sets the holder only when none is set, and Free removes the row.
type fakeRows struct {
	rows      map[string]model.ClaimRow
	extra     []model.ClaimRow
	seen      map[string]time.Time
	fail      map[string]error
	deaf      int
	freeKeeps bool
	calls     map[string]int
	onCall    func(method string, call int)
}

func newFakeRows() *fakeRows {
	return &fakeRows{rows: map[string]model.ClaimRow{}, seen: map[string]time.Time{}, fail: map[string]error{}, calls: map[string]int{}}
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

func (f *fakeRows) LastSeen(context.Context) (map[string]time.Time, error) {
	if err := f.enter("LastSeen"); err != nil {
		return nil, err
	}

	return f.seen, nil
}

func (f *fakeRows) Beat(_ context.Context, holder string) error {
	if err := f.enter("Beat"); err != nil {
		return err
	}

	f.seen[holder] = time.Unix(nowUnix, 0)

	return nil
}

func (f *fakeRows) Row(_ context.Context, item string) (model.RowState, error) {
	if err := f.enter("Row"); err != nil {
		return model.RowState{}, err
	}

	row, found := f.rows[item]

	return model.RowState{Exists: found, Held: found, Holder: row.Holder, Nonce: row.Nonce}, nil
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
		f.rows[row.Item] = row
	}

	return nil
}

func (f *fakeRows) StoreLease(_ context.Context, row model.ClaimRow) error {
	if err := f.enter("StoreLease"); err != nil {
		return err
	}

	f.rows[row.Item] = row

	return nil
}

func (f *fakeRows) Free(_ context.Context, row model.ClaimRow) error {
	if err := f.enter("Free"); err != nil {
		return err
	}

	if current, found := f.rows[row.Item]; found && (current.Holder != row.Holder || current.Nonce != row.Nonce) {
		return model.ErrRowMoved
	}

	if !f.freeKeeps {
		delete(f.rows, row.Item)
	}

	return nil
}

type harness struct {
	t      *testing.T
	rows   *fakeRows
	out    bytes.Buffer
	warn   bytes.Buffer
	slept  []time.Duration
	claims *claim.Claims
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	h := &harness{t: t, rows: newFakeRows()}
	takes := 0
	claims, err := claim.New(claim.Options{
		Rows: h.rows, Lease: lease * time.Second, Settle: 500 * time.Millisecond,
		Clock: func() time.Time { return time.Unix(nowUnix, 0) },
		Sleep: func(d time.Duration) { h.slept = append(h.slept, d) },
		Nonce: func() string { takes++; return fmt.Sprintf("take-%d", takes) },
		Out:   &h.out, Warn: &h.warn,
	})
	require.NoError(t, err)

	h.claims = claims

	return h
}

func (h *harness) said() string {
	return h.out.String()
}

// hold makes who the holder of item with a stored lease, last seen secondsAgo seconds before now. A negative value leaves who without a beat.
func (h *harness) hold(item, who string, storedLease, secondsAgo int64) {
	h.t.Helper()

	h.rows.rows[item] = model.ClaimRow{Item: item, Holder: who, Lease: storedLease}

	if secondsAgo >= 0 {
		h.rows.seen[who] = time.Unix(nowUnix-secondsAgo, 0)
	}
}

func (h *harness) code(err error) int {
	h.t.Helper()

	var failure *claim.Error
	require.ErrorAs(h.t, err, &failure)

	return failure.Code
}
