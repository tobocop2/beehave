package review_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

// pulls is the fake forge of the ready read. A key in fails makes the method of that name fail.
type pulls struct {
	view    model.PullRequest
	changes []model.ChangeRequest
	threads []model.ReviewThread
	fails   map[string]bool
}

func (p *pulls) PRView(_ context.Context, _ int, _ ...string) (model.PullRequest, error) {
	if p.fails["PRView"] {
		return model.PullRequest{}, errDown
	}

	return p.view, nil
}

func (p *pulls) OpenChangeRequests(context.Context, int) ([]model.ChangeRequest, error) {
	if p.fails["OpenChangeRequests"] {
		return nil, errDown
	}

	return p.changes, nil
}

func (p *pulls) UnresolvedThreads(context.Context, int) ([]model.ReviewThread, error) {
	if p.fails["UnresolvedThreads"] {
		return nil, errDown
	}

	return p.threads, nil
}

func cleanPulls() *pulls {
	return &pulls{view: model.PullRequest{Number: 7, HeadRefOid: str(head), State: str("OPEN"), Mergeable: str("MERGEABLE")}, fails: map[string]bool{}}
}

func TestStanding_ReasonsNameEveryWayThePullRequestIsNotReady(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		change func(*pulls)
		reason string
	}{
		"a_review_asks_for_changes": {func(p *pulls) { p.changes = []model.ChangeRequest{{Login: "rev", Commit: old}} }, "a review asks for changes"},
		"a_thread_is_unresolved":    {func(p *pulls) { p.threads = make([]model.ReviewThread, 2) }, "2 review threads are unresolved"},
		"the_branch_conflicts":      {func(p *pulls) { p.view.Mergeable = str("CONFLICTING") }, "it conflicts with its base branch"},
		"the_head_moved":            {func(p *pulls) { p.view.HeadRefOid = str(old) }, `it moved to "` + old + `" after the review read "` + head + `"`},
		"the_view_fails":            {func(p *pulls) { p.fails["PRView"] = true }, "the pull request could not be read"},
		"the_changes_read_fails":    {func(p *pulls) { p.fails["OpenChangeRequests"] = true }, "the change requests could not be read"},
		"the_threads_read_fails":    {func(p *pulls) { p.fails["UnresolvedThreads"] = true }, "the review threads could not be read"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := cleanPulls()
			tt.change(p)

			assert.Contains(t, review.Pull(t.Context(), p, 7).Reasons(head), tt.reason)
		})
	}
}

func TestStanding_ACleanPullRequestHasNoReason(t *testing.T) {
	t.Parallel()

	standing := review.Pull(t.Context(), cleanPulls(), 7)

	assert.Empty(t, standing.Reasons(head))
	assert.Equal(t, 7, standing.Number)
	assert.Equal(t, head, standing.Head)
}

func TestStanding_ReasonsKeepTheOrderOfTheChecks(t *testing.T) {
	t.Parallel()

	p := cleanPulls()
	p.changes = []model.ChangeRequest{{Login: "rev", Commit: old}}
	p.view.Mergeable = str("CONFLICTING")

	require.Equal(t, []string{"a review asks for changes", "it conflicts with its base branch"}, review.Pull(t.Context(), p, 7).Reasons(head))
}
