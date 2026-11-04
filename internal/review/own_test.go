package review_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func ownPull(over func(*model.OwnPull)) model.OwnPull {
	p := model.OwnPull{Number: 4, Push: "2026-10-19T00:00:00Z", ChangesKnown: true, ThreadsKnown: true}
	if over != nil {
		over(&p)
	}

	return p
}

func ownLines(t *testing.T, pulls ...model.OwnPull) []string {
	t.Helper()

	w := newWorld()
	w.own = pulls

	rows, err := poll(t, w)
	require.NoError(t, err)

	return lines(t, rows)
}

func TestOwn_AFreshReviewAndAnOpenThreadAreListed(t *testing.T) {
	t.Parallel()

	got := ownLines(t, ownPull(func(p *model.OwnPull) {
		p.Reviews = []model.ReviewStamp{{Login: "alice", SubmittedAt: "2026-10-20T00:00:00Z"}}
		p.OpenThreads = 2
	}))
	assert.Equal(t, []string{"OWN        #4 fresh_reviews=1 unresolved=2 changes_requested=none draft=false"}, got)
}

func TestOwn_AReviewAtThePushTimeOrByUsIsNotFresh(t *testing.T) {
	t.Parallel()

	got := ownLines(t, ownPull(func(p *model.OwnPull) {
		p.Reviews = []model.ReviewStamp{
			{Login: "alice", SubmittedAt: "2026-10-19T00:00:00Z"},
			{Login: me, SubmittedAt: "2026-10-20T00:00:00Z"},
			{Login: "bob", SubmittedAt: ""},
		}
	}))
	assert.Empty(t, got)
}

func TestOwn_AStandingChangeRequestIsListedWithItsCommit(t *testing.T) {
	t.Parallel()

	got := ownLines(t, ownPull(func(p *model.OwnPull) {
		p.ChangeRequests = []model.ChangeRequest{{Login: "rev", Commit: "eeeeeeeeeeeeeeeeeeee"}}
		p.Draft = true
	}))
	assert.Equal(t, []string{"OWN        #4 fresh_reviews=0 unresolved=0 changes_requested=rev@eeeeeeeee draft=true"}, got)
}

func TestOwn_MoreThreadsThanOnePageReadAsUnknown(t *testing.T) {
	t.Parallel()

	got := ownLines(t, ownPull(func(p *model.OwnPull) { p.ThreadsKnown = false }))
	assert.Equal(t, []string{"OWN        #4 fresh_reviews=0 unresolved=unknown changes_requested=none draft=false"}, got)
}

func TestOwn_APullRequestWithNoPushTimeCountsEveryReviewByOthers(t *testing.T) {
	t.Parallel()

	got := ownLines(t, ownPull(func(p *model.OwnPull) {
		p.Push = ""
		p.Reviews = []model.ReviewStamp{{Login: "alice", SubmittedAt: "2026-10-01T00:00:00Z"}}
	}))
	assert.Contains(t, got[0], "fresh_reviews=1")
}

func TestOwn_UnknownChangeRequestsStopThePoll(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.own = []model.OwnPull{ownPull(func(p *model.OwnPull) { p.ChangesKnown = false })}

	_, err := poll(t, w)
	require.ErrorContains(t, err, "cannot read the reviews of #4")
}

func TestOwn_AFailedOwnReadStopsThePoll(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.fails["OwnPulls"] = true

	_, err := poll(t, w)
	require.ErrorIs(t, err, errDown)
}
