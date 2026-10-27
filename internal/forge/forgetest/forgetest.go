// Package forgetest holds the conformance suite for forge.Forge. An adapter test runs the suite against the adapter with a fake command line.
// Every new forge backend must call the suite from its tests.
package forgetest

import (
	"context"
	"fmt"
	"testing"

	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/model"
)

const pageSize = 10

// Reporter is the part of testing.T the suite uses. A test of the suite passes a recorder to see a failure.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// Factory returns a new forge over a fake that serves the pull request that Seven describes. When broken is true, every command fails.
type Factory func(t *testing.T, broken bool) forge.Forge

// SevenReviewer, SevenBody and SevenTitle describe pull request 7, which the fake behind a Factory serves.
const (
	Seven         = 7
	SevenReviewer = "alice"
	SevenBody     = "Closes #5"
	SevenTitle    = "Add the adapters"
	SevenCheck    = "build"
)

// TestForge runs the conformance suite against the forge that make returns, as golang.org/x/net/nettest.TestConn does for a connection.
func TestForge(t *testing.T, make Factory) {
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
	r Reporter
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

// call is one method of the contract with its arguments filled in.
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

// CheckForgeReads runs the read rules on a forge that serves pull request 7.
func CheckForgeReads(r Reporter, target forge.Forge) {
	r.Helper()

	c, ctx := check{r: r}, context.Background()

	reviews, err := target.Reviews(ctx, Seven)
	c.noError(err, "Reviews")
	c.equal(pick(reviews, func(v model.Review) string { return v.User.Login + " " + string(v.State) }), []string{SevenReviewer + " APPROVED"}, "reviews")

	body, err := target.PRBody(ctx, Seven)
	c.noError(err, "PRBody")
	c.equal(body, SevenBody, "body")

	view, err := target.PRView(ctx, Seven, "title")
	c.noError(err, "PRView")
	c.equal(view.Title, SevenTitle, "title")

	checkLists(c, target)
}

func checkLists(c check, target forge.Forge) {
	c.r.Helper()

	ctx := context.Background()

	list, err := target.PRList(ctx, pageSize, "--json", "number,title")
	c.noError(err, "PRList")
	c.equal(pick(list, func(v model.PullRequest) int { return v.Number }), []int{Seven}, "listed numbers")

	checks, err := target.PRChecks(ctx, Seven)
	c.noError(err, "PRChecks")
	c.equal(checks, []model.Check{{Name: SevenCheck, Bucket: model.CheckPass}}, "checks")
}

// CheckForgeFails runs every method of a forge whose commands all fail. Each must return an error and never a guess.
func CheckForgeFails(r Reporter, target forge.Forge) {
	r.Helper()

	check{r: r}.allFail([]call{
		{"Reviews", func(ctx context.Context) error { _, err := target.Reviews(ctx, Seven); return err }},
		{"PRBody", func(ctx context.Context) error { _, err := target.PRBody(ctx, Seven); return err }},
		{"PRView", func(ctx context.Context) error { _, err := target.PRView(ctx, Seven, "title"); return err }},
		{"PRList", func(ctx context.Context) error {
			_, err := target.PRList(ctx, pageSize, "--json", "number")
			return err
		}},
		{"PRChecks", func(ctx context.Context) error { _, err := target.PRChecks(ctx, Seven); return err }},
		{"OpenChangeRequests", func(ctx context.Context) error { _, err := target.OpenChangeRequests(ctx, Seven); return err }},
		{"UnresolvedThreads", func(ctx context.Context) error { _, err := target.UnresolvedThreads(ctx, Seven); return err }},
		{"StackID", func(ctx context.Context) error { _, err := target.StackID(ctx, Seven); return err }},
		{"ExistingReview", func(ctx context.Context) error { _, err := target.ExistingReview(ctx, Seven); return err }},
		{"Overlap", func(ctx context.Context) error {
			_, err := target.Overlap(ctx, model.OverlapRequest{PR: "7"})
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
