// Package storetest holds the conformance suites for store.Tracker and store.ClaimRows. An adapter test runs a suite against the adapter with a fake command line.
// Every new store backend must call the suites from its tests.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store"
)

// ItemID is the item that a tracker under test serves when it is not broken.
const ItemID = "5"

const (
	item    = ItemID
	holderA = "session-a"
	holderB = "session-b"
)

// Reporter is the part of testing.T the suites use. A test of a suite passes a recorder to see a failure.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// TrackerFactory returns a new tracker over a fake that serves the item ItemID. When broken is true, every command of the fake fails.
type TrackerFactory func(t *testing.T, broken bool) store.Tracker

// TestTracker runs the conformance suite against the tracker that make returns, as testing/fstest.TestFS does for a file system.
func TestTracker(t *testing.T, make TrackerFactory) {
	t.Helper()
	t.Run("declares_the_item_fields", func(t *testing.T) {
		CheckItemFields(t, make(t, false))
	})
	t.Run("item_returns_the_declared_fields", func(t *testing.T) {
		CheckItem(t, make(t, false))
	})
	t.Run("a_failed_command_is_an_error_everywhere", func(t *testing.T) {
		CheckTrackerFails(t, make(t, true))
	})
}

// ClaimRowsFactory returns new claim rows over an empty fake. When broken is true, every command of the fake fails.
type ClaimRowsFactory func(t *testing.T, broken bool) store.ClaimRows

// TestClaimRows runs the conformance suite against the claim rows that make returns.
func TestClaimRows(t *testing.T, make ClaimRowsFactory) {
	t.Helper()
	t.Run("claim_rows", func(t *testing.T) {
		CheckClaimRows(t, make(t, false))
	})
	t.Run("a_failed_command_is_an_error_everywhere", func(t *testing.T) {
		CheckClaimRowsFail(t, make(t, true))
	})
}

// check records a failed expectation.
type check struct {
	r    Reporter
	rows store.ClaimRows
}

func (c check) noError(err error, what string) {
	c.r.Helper()

	if err != nil {
		c.r.Errorf("%s: unexpected error: %v", what, err)
	}
}

func (c check) isError(err error, what string) {
	c.r.Helper()

	if err == nil {
		c.r.Errorf("%s: want an error from a failed command, got none", what)
	}
}

func (c check) equal(got, want any, what string) {
	c.r.Helper()

	if fmt.Sprintf("%#v", got) != fmt.Sprintf("%#v", want) {
		c.r.Errorf("%s: got %v, want %v", what, got, want)
	}
}

// CheckClaimRows runs the claim row rules on a tracker with no rows.
func CheckClaimRows(r Reporter, rows store.ClaimRows) {
	r.Helper()

	c := check{r: r, rows: rows}
	c.emptyTracker()
	c.holdRules()
	c.beatRules()
	c.reclaimRules()
	c.freeRules()
}

func (c check) held() []model.ClaimRow {
	c.r.Helper()

	held, err := c.rows.HeldRows(context.Background())
	c.noError(err, "HeldRows")

	return held
}

// holders returns the holders of the item, in the order HeldRows lists them.
func (c check) holders() []string {
	c.r.Helper()

	var names []string

	for _, row := range c.held() {
		if row.Item == item {
			names = append(names, row.Holder)
		}
	}

	return names
}

func (c check) emptyTracker() {
	c.r.Helper()

	c.equal(len(c.held()), 0, "held rows of an empty tracker")
}

func (c check) holdRules() {
	c.r.Helper()

	ctx := context.Background()
	c.noError(c.rows.TryHold(ctx, model.ClaimRow{Item: item, Holder: holderA}), "TryHold of a free row")
	c.equal(c.holders(), []string{holderA}, "holders after TryHold")
	c.equal(c.held()[0].LeaseEnds.IsZero(), false, "a take carries the end of its lease")

	c.noError(c.rows.TryHold(ctx, model.ClaimRow{Item: item, Holder: holderB}), "TryHold of a held row")
	c.equal(c.holders(), []string{holderA}, "a second TryHold leaves the first holder")
}

func (c check) beatRules() {
	c.r.Helper()

	ctx := context.Background()
	c.noError(c.rows.Beat(ctx, holderA), "Beat")
	c.noError(c.rows.Beat(ctx, holderA), "Beat again")
	c.equal(errors.Is(c.rows.Beat(ctx, holderB), model.ErrNoClaim), true, "Beat of a holder with no row returns ErrNoClaim")
}

func (c check) reclaimRules() {
	c.r.Helper()

	freed, err := c.rows.Reclaim(context.Background(), item)
	c.noError(err, "Reclaim of a live lease")
	c.equal(freed, false, "Reclaim leaves a row whose lease has not ended")
	c.equal(c.holders(), []string{holderA}, "holders after Reclaim of a live lease")
}

func (c check) freeRules() {
	c.r.Helper()

	ctx := context.Background()
	c.noError(c.rows.Free(ctx, model.ClaimRow{Item: item, Holder: holderA}), "Free")
	c.noError(c.rows.Free(ctx, model.ClaimRow{Item: item, Holder: holderA}), "Free of a free row")
	c.equal(len(c.held()), 0, "held rows after Free")

	c.noError(c.rows.TryHold(ctx, model.ClaimRow{Item: item, Holder: holderB}), "TryHold of a freed row")
	c.equal(c.holders(), []string{holderB}, "a freed row can be held by another")

	moved := c.rows.Free(ctx, model.ClaimRow{Item: item, Holder: holderA})
	c.equal(errors.Is(moved, model.ErrRowMoved), true, "Free of a row that another holder holds returns ErrRowMoved")
	c.equal(c.holders(), []string{holderB}, "Free of another holder's row leaves it")
}

// call is one method of a contract with its arguments filled in.
type call struct {
	name string
	run  func(ctx context.Context) error
}

func (c check) allFail(calls []call) {
	c.r.Helper()

	for _, one := range calls {
		c.isError(one.run(context.Background()), one.name)
	}
}

// CheckItemFields checks that a tracker declares the fields every tracker must carry: id, title and body.
func CheckItemFields(r Reporter, tracker store.Tracker) {
	r.Helper()

	declared := tracker.ItemFields()

	for _, want := range []string{"id", "title", "body"} {
		if !slices.Contains(declared, want) {
			r.Errorf("ItemFields: want %q among the declared fields, got %v", want, declared)
		}
	}
}

// CheckItem checks the success path of Item on a tracker that serves the item ItemID: the result holds one value for each declared field, and the id is the item.
func CheckItem(r Reporter, tracker store.Tracker) {
	r.Helper()

	got, err := tracker.Item(context.Background(), ItemID)
	if err != nil {
		r.Errorf("Item: unexpected error: %v", err)

		return
	}

	checkDeclared(r, got, tracker.ItemFields())

	if got["id"] != ItemID {
		r.Errorf("Item: want id %q, got %q", ItemID, got["id"])
	}
}

// checkDeclared checks that got holds one value for each declared field and no other field.
func checkDeclared(r Reporter, got map[string]string, declared []string) {
	r.Helper()

	for _, name := range declared {
		if _, found := got[name]; !found {
			r.Errorf("Item: want a value for the declared field %q, got %v", name, got)
		}
	}

	for name := range got {
		if !slices.Contains(declared, name) {
			r.Errorf("Item: got the field %q, which ItemFields does not declare (%v)", name, declared)
		}
	}
}

// CheckTrackerFails runs every method of a tracker whose commands all fail. Each must return an error and never a guess.
func CheckTrackerFails(r Reporter, tracker store.Tracker) {
	r.Helper()

	check{r: r}.allFail(trackerCalls(tracker))
}

func trackerCalls(tracker store.Tracker) []call {
	return []call{
		{"OpenItems", func(ctx context.Context) error { _, err := tracker.OpenItems(ctx); return err }},
		{"Item", func(ctx context.Context) error { _, err := tracker.Item(ctx, item); return err }},
	}
}

// CheckClaimRowsFail runs every method of claim rows whose commands all fail. Each must return an error and never a guess.
func CheckClaimRowsFail(r Reporter, rows store.ClaimRows) {
	r.Helper()

	check{r: r}.allFail(rowCalls(rows))
}

func rowCalls(rows store.ClaimRows) []call {
	hold := model.ClaimRow{Item: item, Holder: holderA}

	return []call{
		{"HeldRows", func(ctx context.Context) error { _, err := rows.HeldRows(ctx); return err }},
		{"Beat", func(ctx context.Context) error { return rows.Beat(ctx, holderA) }},
		{"TryHold", func(ctx context.Context) error { return rows.TryHold(ctx, hold) }},
		{"Reclaim", func(ctx context.Context) error { _, err := rows.Reclaim(ctx, item); return err }},
		{"Free", func(ctx context.Context) error { return rows.Free(ctx, hold) }},
	}
}
