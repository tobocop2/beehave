// Package contract holds the conformance suites for model.Tracker, model.ClaimRows and model.Forge. An adapter test runs a suite against the adapter with a fake command line.
package contract

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/tobocop2/obee/internal/model"
)

const (
	item     = "5"
	holderA  = "session-a"
	holderB  = "session-b"
	bodyMode = 0o600
	pageSize = 10
)

// Reporter is the part of testing.T the suites use. A test of a suite passes a recorder to see a failure.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// TrackerFactory returns a new tracker over an empty fake. When broken is true, every command of the fake fails.
type TrackerFactory func(t *testing.T, broken bool) model.Tracker

// TestTracker runs the conformance suite against the tracker that make returns, as testing/fstest.TestFS does for a file system.
func TestTracker(t *testing.T, make TrackerFactory) {
	t.Helper()
	t.Run("a_failed_command_is_an_error_everywhere", func(t *testing.T) {
		CheckTrackerFails(t, make(t, true), filepath.Join(t.TempDir(), "body.md"))
	})
}

// ClaimRowsFactory returns new claim rows over an empty fake. When broken is true, every command of the fake fails.
type ClaimRowsFactory func(t *testing.T, broken bool) model.ClaimRows

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

// ForgeFactory returns a new forge over a fake that serves the pull request that Seven describes. When broken is true, every command fails.
type ForgeFactory func(t *testing.T, broken bool) model.Forge

// SevenReviewer, SevenBody and SevenTitle describe pull request 7, which the fake behind a ForgeFactory serves.
const (
	Seven         = 7
	SevenReviewer = "alice"
	SevenBody     = "Closes #5"
	SevenTitle    = "Add the adapters"
	SevenCheck    = "build"
)

// TestForge runs the conformance suite against the forge that make returns, as golang.org/x/net/nettest.TestConn does for a connection.
func TestForge(t *testing.T, make ForgeFactory) {
	t.Helper()
	t.Run("reads_the_pull_request", func(t *testing.T) {
		CheckForgeReads(t, make(t, false))
	})
	t.Run("a_failed_command_is_an_error_everywhere", func(t *testing.T) {
		CheckForgeFails(t, make(t, true))
	})
}

// check records a failed expectation.
type check struct {
	r    Reporter
	rows model.ClaimRows
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
func CheckClaimRows(r Reporter, rows model.ClaimRows) {
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

// CheckTrackerFails runs every method of a tracker whose commands all fail. Each must return an error and never a guess.
func CheckTrackerFails(r Reporter, tracker model.Tracker, bodyFile string) {
	r.Helper()

	writeBody(r, bodyFile)

	check{r: r}.allFail(trackerCalls(tracker, bodyFile))
}

func trackerCalls(tracker model.Tracker, bodyFile string) []call {
	request := model.CreateRequest{Title: "a finding", BodyFile: bodyFile, Severity: model.SeverityP2}

	return []call{
		{"OpenItems", func(ctx context.Context) error { _, err := tracker.OpenItems(ctx); return err }},
		{"Containers", func(ctx context.Context) error { _, err := tracker.Containers(ctx); return err }},
		{"Children", func(ctx context.Context) error { _, err := tracker.Children(ctx, item); return err }},
		{"Show", func(ctx context.Context) error { _, err := tracker.Show(ctx, item); return err }},
		{"Close", func(ctx context.Context) error { return tracker.Close(ctx, item, "done") }},
		{"Create", func(ctx context.Context) error { _, err := tracker.Create(ctx, request); return err }},
	}
}

// CheckClaimRowsFail runs every method of claim rows whose commands all fail. Each must return an error and never a guess.
func CheckClaimRowsFail(r Reporter, rows model.ClaimRows) {
	r.Helper()

	check{r: r}.allFail(rowCalls(rows))
}

func rowCalls(rows model.ClaimRows) []call {
	hold := model.ClaimRow{Item: item, Holder: holderA}

	return []call{
		{"HeldRows", func(ctx context.Context) error { _, err := rows.HeldRows(ctx); return err }},
		{"Beat", func(ctx context.Context) error { return rows.Beat(ctx, holderA) }},
		{"TryHold", func(ctx context.Context) error { return rows.TryHold(ctx, hold) }},
		{"Reclaim", func(ctx context.Context) error { _, err := rows.Reclaim(ctx, item); return err }},
		{"Free", func(ctx context.Context) error { return rows.Free(ctx, hold) }},
	}
}

func writeBody(r Reporter, path string) {
	r.Helper()

	if err := os.WriteFile(path, []byte("finding\n"), bodyMode); err != nil {
		r.Errorf("cannot write the body file: %v", err)
	}
}

// CheckForgeReads runs the read rules on a forge that serves pull request 7.
func CheckForgeReads(r Reporter, forge model.Forge) {
	r.Helper()

	c, ctx := check{r: r}, context.Background()

	reviews, err := forge.Reviews(ctx, Seven)
	c.noError(err, "Reviews")
	c.equal(pick(reviews, func(v model.Review) string { return v.User.Login + " " + string(v.State) }), []string{SevenReviewer + " APPROVED"}, "reviews")

	body, err := forge.PRBody(ctx, Seven)
	c.noError(err, "PRBody")
	c.equal(body, SevenBody, "body")

	view, err := forge.PRView(ctx, Seven, "title")
	c.noError(err, "PRView")
	c.equal(view.Title, SevenTitle, "title")

	checkLists(c, forge)
}

func checkLists(c check, forge model.Forge) {
	c.r.Helper()

	ctx := context.Background()

	list, err := forge.PRList(ctx, pageSize, "--json", "number,title")
	c.noError(err, "PRList")
	c.equal(pick(list, func(v model.PullRequest) int { return v.Number }), []int{Seven}, "listed numbers")

	checks, err := forge.PRChecks(ctx, Seven)
	c.noError(err, "PRChecks")
	c.equal(checks, []model.Check{{Name: SevenCheck, Bucket: model.CheckPass}}, "checks")
}

// CheckForgeFails runs every method of a forge whose commands all fail. Each must return an error and never a guess.
func CheckForgeFails(r Reporter, forge model.Forge) {
	r.Helper()

	check{r: r}.allFail([]call{
		{"Reviews", func(ctx context.Context) error { _, err := forge.Reviews(ctx, Seven); return err }},
		{"PRBody", func(ctx context.Context) error { _, err := forge.PRBody(ctx, Seven); return err }},
		{"PRView", func(ctx context.Context) error { _, err := forge.PRView(ctx, Seven, "title"); return err }},
		{"PRList", func(ctx context.Context) error { _, err := forge.PRList(ctx, pageSize, "--json", "number"); return err }},
		{"PRChecks", func(ctx context.Context) error { _, err := forge.PRChecks(ctx, Seven); return err }},
		{"OpenChangeRequests", func(ctx context.Context) error { _, err := forge.OpenChangeRequests(ctx, Seven); return err }},
		{"UnresolvedThreads", func(ctx context.Context) error { _, err := forge.UnresolvedThreads(ctx, Seven); return err }},
		{"StackID", func(ctx context.Context) error { _, err := forge.StackID(ctx, Seven); return err }},
		{"ExistingReview", func(ctx context.Context) error { _, err := forge.ExistingReview(ctx, Seven); return err }},
		{"Overlap", func(ctx context.Context) error {
			_, err := forge.Overlap(ctx, model.OverlapRequest{PR: "7"})
			return err
		}},
	})
}

func pick[T, V any](rows []T, field func(T) V) []V {
	out := make([]V, len(rows))
	for i, row := range rows {
		out[i] = field(row)
	}

	return out
}
