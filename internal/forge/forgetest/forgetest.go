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
	// SevenCommits and SevenFiles are the counts the fake serves for pull request 7.
	SevenCommits = 2
	SevenFiles   = 3
)

// What the fake behind a Factory also serves: the comment and the inline comment of pull request 7, the default branch,
// the branch of pull request 7 with its tip, and a branch that does not exist.
const (
	SevenComment  = "looks good"
	SevenInline   = "rename this"
	DefaultBranch = "main"
	SevenBranch   = "feature"
	SevenTip      = "0123456789abcdef0123456789abcdef01234567"
	GoneBranch    = "gone"
	NewBranch     = "next"
	ScopeRepo     = "repo"
	ScopeWorkflow = "workflow"
	SearchSince   = "2026-01-01"
	SearchUser    = "alice"
	// SevenRun is the link of a check run that the fake serves with no annotation.
	SevenRun = "https://github.com/owner/name/runs/9"
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
	checkConversation(c, target)
	checkBranches(c, target)
	checkStanding(c, target)
}

// checkCounts runs the read of the forge's own counts.
func checkCounts(c check, target forge.Forge) {
	c.r.Helper()

	commits, files, err := target.PullCounts(context.Background(), Seven)
	c.noError(err, "PullCounts")
	c.equal([]int{commits, files}, []int{SevenCommits, SevenFiles}, "counts")
}

// checkStanding runs the batched read of our pull requests, the title of an item and the annotations of a check run.
func checkStanding(c check, target forge.Forge) {
	c.r.Helper()

	checkCounts(c, target)

	ctx := context.Background()

	title, err := target.Title(ctx, Seven)
	c.noError(err, "Title")
	c.equal(title, SevenTitle, "item title")

	own, err := target.OwnPulls(ctx, SearchUser)
	c.noError(err, "OwnPulls")
	c.equal(pick(own, func(v model.OwnPull) int { return v.Number }), []int{Seven}, "own numbers")

	notStarted, err := target.CheckNotStarted(ctx, SevenRun)
	c.noError(err, "CheckNotStarted")
	c.equal(notStarted, false, "check not started")
}

func checkConversation(c check, target forge.Forge) {
	c.r.Helper()

	ctx := context.Background()
	body := func(v model.Comment) string { return v.Body }

	comments, err := target.Comments(ctx, Seven)
	c.noError(err, "Comments")
	c.equal(pick(comments, body), []string{SevenComment}, "comments")

	inline, err := target.ReviewComments(ctx, Seven)
	c.noError(err, "ReviewComments")
	c.equal(pick(inline, body), []string{SevenInline}, "review comments")

	found, err := target.Search(ctx, model.SearchRequest{Kind: model.SearchPulls, Involves: SearchUser, Since: SearchSince, Limit: pageSize})
	c.noError(err, "Search")
	c.equal(found, []int{Seven}, "searched numbers")

	checkWrites(c, target)
}

func checkWrites(c check, target forge.Forge) {
	c.r.Helper()

	ctx := context.Background()

	c.noError(target.PostComment(ctx, Seven, "hello"), "PostComment")
	c.noError(target.SetDraft(ctx, Seven, true), "SetDraft to draft")
	c.noError(target.SetDraft(ctx, Seven, false), "SetDraft to ready")
	c.noError(target.MergePR(ctx, Seven, SevenTip), "MergePR")
	c.noError(target.CreateBranch(ctx, NewBranch, SevenTip), "CreateBranch")
	c.noError(target.SetBase(ctx, Seven, NewBranch), "SetBase")
	c.noError(target.SetBody(ctx, Seven, "new body"), "SetBody")
}

func checkBranches(c check, target forge.Forge) {
	c.r.Helper()

	ctx := context.Background()

	name, err := target.DefaultBranch(ctx)
	c.noError(err, "DefaultBranch")
	c.equal(name, DefaultBranch, "default branch")

	tip, found, err := target.BranchRef(ctx, SevenBranch)
	c.noError(err, "BranchRef")
	c.equal([]any{tip, found}, []any{SevenTip, true}, "branch tip")

	tip, found, err = target.BranchRef(ctx, GoneBranch)
	c.noError(err, "BranchRef of a missing branch")
	c.equal([]any{tip, found}, []any{"", false}, "missing branch")

	scopes, err := target.TokenScopes(ctx)
	c.noError(err, "TokenScopes")
	c.equal(scopes, []string{ScopeRepo, ScopeWorkflow}, "token scopes")
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
		{"PullCounts", func(ctx context.Context) error { _, _, err := target.PullCounts(ctx, Seven); return err }},
		{"PRChecks", func(ctx context.Context) error { _, err := target.PRChecks(ctx, Seven); return err }},
		{"OpenChangeRequests", func(ctx context.Context) error { _, err := target.OpenChangeRequests(ctx, Seven); return err }},
		{"UnresolvedThreads", func(ctx context.Context) error { _, err := target.UnresolvedThreads(ctx, Seven); return err }},
		{"StackID", func(ctx context.Context) error { _, err := target.StackID(ctx, Seven); return err }},
		{"ExistingReview", func(ctx context.Context) error { _, err := target.ExistingReview(ctx, Seven); return err }},
	})
	checkNewFail(r, target)
	checkMergeFail(r, target)
	checkStandingFail(r, target)
}

// checkStandingFail runs the batched read, the title read and the annotation read on a forge whose commands all fail.
func checkStandingFail(r Reporter, target forge.Forge) {
	r.Helper()

	check{r: r}.allFail([]call{
		{"Title", func(ctx context.Context) error { _, err := target.Title(ctx, Seven); return err }},
		{"OwnPulls", func(ctx context.Context) error { _, err := target.OwnPulls(ctx, SearchUser); return err }},
		{"CheckNotStarted", func(ctx context.Context) error { _, err := target.CheckNotStarted(ctx, SevenRun); return err }},
	})
}

// checkNewFail runs the overlap, the conversation and the branch methods on a forge whose commands all fail.
func checkNewFail(r Reporter, target forge.Forge) {
	r.Helper()

	check{r: r}.allFail([]call{
		{"Overlap", func(ctx context.Context) error {
			_, err := target.Overlap(ctx, model.OverlapRequest{PR: "7"})
			return err
		}},
		{"Comments", func(ctx context.Context) error { _, err := target.Comments(ctx, Seven); return err }},
		{"ReviewComments", func(ctx context.Context) error { _, err := target.ReviewComments(ctx, Seven); return err }},
		{"Search", func(ctx context.Context) error {
			_, err := target.Search(ctx, model.SearchRequest{Kind: model.SearchIssues, Involves: SearchUser, Since: SearchSince, Limit: pageSize})
			return err
		}},
		{"DefaultBranch", func(ctx context.Context) error { _, err := target.DefaultBranch(ctx); return err }},
		{"BranchRef", func(ctx context.Context) error { _, _, err := target.BranchRef(ctx, SevenBranch); return err }},
		{"PostComment", func(ctx context.Context) error { return target.PostComment(ctx, Seven, "hello") }},
		{"SetDraft", func(ctx context.Context) error { return target.SetDraft(ctx, Seven, true) }},
	})
}

// checkMergeFail runs the merge, the branch and pull request edits and the scope read on a forge whose commands all fail.
func checkMergeFail(r Reporter, target forge.Forge) {
	r.Helper()

	check{r: r}.allFail([]call{
		{"MergePR", func(ctx context.Context) error { return target.MergePR(ctx, Seven, SevenTip) }},
		{"CreateBranch", func(ctx context.Context) error { return target.CreateBranch(ctx, NewBranch, SevenTip) }},
		{"SetBase", func(ctx context.Context) error { return target.SetBase(ctx, Seven, NewBranch) }},
		{"SetBody", func(ctx context.Context) error { return target.SetBody(ctx, Seven, "new body") }},
		{"TokenScopes", func(ctx context.Context) error { _, err := target.TokenScopes(ctx); return err }},
	})
}

func pick[T, V any](rows []T, field func(T) V) []V {
	out := make([]V, len(rows))
	for i, row := range rows {
		out[i] = field(row)
	}

	return out
}
