package review_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

const (
	me   = "me"
	head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	old  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var (
	errDown = errors.New("down")
	today   = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
)

// world is the fake forge. A key in fails makes the method of that name fail.
type world struct {
	lists          map[string][]int
	views          map[int]model.PullRequest
	reviews        map[int][]model.Review
	issueComments  map[int][]model.Comment
	reviewComments map[int][]model.Comment
	searches       map[model.SearchKind][]int
	checks         map[int][]model.Check
	titles         map[int]string
	notStarted     map[string]bool
	own            []model.OwnPull
	stacks         map[int]string
	defaultBranch  string
	fails          map[string]bool
	asked          []model.SearchRequest
	lookups        atomic.Int32 // the calls of DefaultBranch
}

func newWorld() *world {
	return &world{
		lists: map[string][]int{}, views: map[int]model.PullRequest{}, reviews: map[int][]model.Review{},
		issueComments: map[int][]model.Comment{}, reviewComments: map[int][]model.Comment{},
		searches: map[model.SearchKind][]int{}, checks: map[int][]model.Check{}, titles: map[int]string{},
		notStarted: map[string]bool{}, stacks: map[int]string{}, fails: map[string]bool{},
	}
}

func (w *world) fail(name string) error {
	if w.fails[name] {
		return errDown
	}

	return nil
}

func (w *world) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	if w.fails["PRList"] || (w.fails["PRListOurs"] && slices.Contains(args, "number,isDraft")) {
		return nil, errDown
	}

	var rows []model.PullRequest

	for _, n := range w.lists[strings.Join(args, " ")] {
		rows = append(rows, model.PullRequest{Number: n, IsDraft: draftOf(w.views[n])})
	}

	return rows, nil
}

func draftOf(p model.PullRequest) *bool { return p.IsDraft }

func (w *world) PRView(_ context.Context, pr int, _ ...string) (model.PullRequest, error) {
	return w.views[pr], w.fail("PRView")
}

func (w *world) Reviews(_ context.Context, pr int) ([]model.Review, error) {
	return w.reviews[pr], w.fail("Reviews")
}

func (w *world) Comments(_ context.Context, n int) ([]model.Comment, error) {
	return w.issueComments[n], w.fail("Comments")
}

func (w *world) ReviewComments(_ context.Context, n int) ([]model.Comment, error) {
	return w.reviewComments[n], w.fail("ReviewComments")
}

func (w *world) Search(_ context.Context, req model.SearchRequest) ([]int, error) {
	w.asked = append(w.asked, req)

	if len(w.searches[req.Kind]) >= req.Limit {
		return nil, errors.New("the search may be cut off")
	}

	return w.searches[req.Kind], w.fail("Search")
}

func (w *world) PRChecks(_ context.Context, pr int) ([]model.Check, error) {
	return w.checks[pr], w.fail("PRChecks")
}

func (w *world) Title(_ context.Context, n int) (string, error) { return w.titles[n], w.fail("Title") }

func (w *world) CheckNotStarted(_ context.Context, link string) (bool, error) {
	return w.notStarted[link], w.fail("CheckNotStarted")
}

func (w *world) OwnPulls(context.Context, string) ([]model.OwnPull, error) {
	return w.own, w.fail("OwnPulls")
}

func (w *world) StackID(_ context.Context, pr int) (string, error) {
	return w.stacks[pr], w.fail("StackID")
}

func (w *world) DefaultBranch(context.Context) (string, error) {
	w.lookups.Add(1)

	return w.defaultBranch, w.fail("DefaultBranch")
}

func str(s string) *string { return &s }

func lines(t *testing.T, rows []review.Row) []string {
	t.Helper()

	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = row.String()
	}

	return out
}

func review1(login string, state model.ReviewState, commit, at string) model.Review {
	return model.Review{User: model.Account{Login: login}, State: state, CommitID: commit, SubmittedAt: str(at)}
}

func comment(login, at string) model.Comment {
	return model.Comment{User: model.Account{Login: login}, CreatedAt: at}
}

func pullView(number int, over func(*model.PullRequest)) model.PullRequest {
	f := false
	p := model.PullRequest{
		Number: number, Title: strings.Repeat("T", 80), Author: model.Account{Login: "alice"},
		HeadRefOid: str(head), IsDraft: &f, UpdatedAt: "2026-10-20T10:00:00Z",
	}

	if over != nil {
		over(&p)
	}

	return p
}

// requested is the key of the review-requested listing of the poll.
const requested = "--state open --search review-requested:me -author:me --json number"
