package batch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// status is the status that a green gate posts for one member head.
func status(contextName, head, description string) model.CommitStatus {
	return model.CommitStatus{SHA: head, Context: contextName, State: model.StatusSuccess, Description: description}
}

func TestRun_AGreenGatePostsTheGateStatusOnEachMemberHeadAndTheReviewStatusOnApprovedHeads(t *testing.T) {
	t.Parallel()

	w := newWorld()
	list := members("a", "b")
	list[0].Approved = true

	res, err := Run(context.Background(), w.deps(), list)

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, res.Merged)
	assert.Equal(t, []model.CommitStatus{
		status("beehave/review", "h-a", "The review approved this head."),
		status("beehave/gate", "h-a", "The gate passed on the union commit c:a,b."),
		status("beehave/gate", "h-b", "The gate passed on the union commit c:a,b."),
	}, w.posted)
}

func TestRun_ARedGatePostsNoStatusAndAGreenSplitPostsOnlyTheHeadsThatThisGateRanOn(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.bad = func(items []string) bool { return len(items) > 0 && contains(items, "b") }

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, res.Merged)
	assert.Equal(t, []model.CommitStatus{status("beehave/gate", "h-a", "The gate passed on the union commit c:a.")}, w.posted, "the red member b and the red union of a and b get no status")
}

func TestRun_ANewHeadOfAMemberGetsItsOwnStatusAndTheOldHeadIsNotPostedAgain(t *testing.T) {
	t.Parallel()

	w := newWorld()
	_, err := Run(context.Background(), w.deps(), members("a"))
	require.NoError(t, err)

	pushed := newWorld()
	pushed.items = nil
	moved := []Member{{Item: "a", Head: "h-a2"}}

	_, err = Run(context.Background(), pushed.deps(), moved)
	require.NoError(t, err)
	require.Len(t, pushed.posted, 1)
	assert.Equal(t, "h-a2", pushed.posted[0].SHA, "the status names the head that the gate ran on")
	assert.NotContains(t, []string{pushed.posted[0].SHA}, w.posted[0].SHA, "the status of the old head stays on the old head")
}

func TestRun_AResumeAfterAMergeThatFailedHandsTheMergeTheSameStatusesAgain(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.failMerge = errBoom
	deps := w.deps()

	_, err := Run(context.Background(), deps, members("a"))
	require.ErrorIs(t, err, errBoom)
	require.Len(t, w.posted, 1)

	w.failMerge = nil

	res, err := Run(context.Background(), deps, members("a"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, res.Merged)
	require.Len(t, w.posted, 2, "the resume posts again, and the forge takes it as the same status")
	assert.Equal(t, w.posted[0], w.posted[1], "the second post carries the same context, state and commit")
}

func TestRun_ABaseThatMovedBeforeAMemberHandsItNoStatus(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.pushAfter = 1

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, res.Merged)
	assert.Equal(t, []string{"h-a"}, headsOf(w.posted), "member b is deferred before its merge, so no status is handed for it")
}

// headsOf lists the commit of each status.
func headsOf(statuses []model.CommitStatus) []string {
	heads := make([]string, 0, len(statuses))
	for _, s := range statuses {
		heads = append(heads, s.SHA)
	}

	return heads
}

func contains(items []string, item string) bool {
	for _, have := range items {
		if have == item {
			return true
		}
	}

	return false
}
