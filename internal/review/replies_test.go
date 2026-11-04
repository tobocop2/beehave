package review_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

const link = "https://github.com/o/n/actions/runs/1/job/"

func TestReplies_AReplyOnAnIssueIsListedWithItsTitle(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.searches[model.SearchIssues] = []int{9}
	w.issueComments[9] = []model.Comment{comment("alice", "2026-10-18T00:00:00Z")}
	w.titles[9] = "T"

	rows, err := poll(t, w)
	require.NoError(t, err)
	assert.Equal(t, []string{"REPLY      #9 at=2026-10-18T00:00:00Z T"}, lines(t, rows))
}

func TestReplies_AnAnsweredItemIsNotListed(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.searches[model.SearchIssues] = []int{9}
	w.issueComments[9] = []model.Comment{comment("alice", "2026-10-18T00:00:00Z"), comment(me, "2026-10-19T00:00:00Z")}

	rows, err := poll(t, w)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestReplies_APullRequestSearchReadsTheReviewPathsAndAFailedTitleShowsNothing(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.searches[model.SearchPulls] = []int{5}
	w.reviews[5] = []model.Review{review1("bob", model.ReviewChangesRequested, head, "2026-10-18T00:00:00Z")}
	w.fails["Title"] = true

	rows, err := poll(t, w)
	require.NoError(t, err)
	assert.Equal(t, []string{"REPLY      #5 at=2026-10-18T00:00:00Z "}, lines(t, rows))
}

func TestReplies_199SearchRowsPassAnd200AreCutOff(t *testing.T) {
	t.Parallel()

	w := newWorld()
	for n := range 199 {
		w.searches[model.SearchIssues] = append(w.searches[model.SearchIssues], 100+n)
	}

	_, err := poll(t, w)
	require.NoError(t, err)

	w.searches[model.SearchIssues] = append(w.searches[model.SearchIssues], 1)
	_, err = poll(t, w)
	require.ErrorContains(t, err, "cannot search issues")
}

func TestReplies_AFailedSearchStopsThePollForEachKind(t *testing.T) {
	t.Parallel()

	for _, kind := range []model.SearchKind{model.SearchIssues, model.SearchPulls} {
		w := newWorld()
		w.searches[kind] = []int{5}
		w.fails["Search"] = true

		rows, err := poll(t, w)
		require.ErrorIs(t, err, errDown, string(kind))
		require.ErrorContains(t, err, "cannot search", string(kind))
		assert.Empty(t, rows, "a search that failed is not an empty list")
	}
}

func TestReplies_AnUnreadableItemStopsThePoll(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"Comments", "ReviewComments", "Reviews"} {
		w := newWorld()
		w.searches[model.SearchPulls] = []int{5}
		w.fails[name] = true

		_, err := poll(t, w)
		require.ErrorIs(t, err, errDown, name)
		assert.ErrorContains(t, err, "cannot read the comments of #5", name)
	}
}

func ready(w *world, checks ...model.Check) {
	w.lists["--state open --author me --json number,isDraft"] = []int{6}
	w.views[6] = pullView(6, nil)
	w.checks[6] = checks
}

func red(suffix, bucket string) model.Check {
	return model.Check{Name: "x", Bucket: model.CheckBucket(bucket), Link: link + suffix}
}

func TestCI_AFailedCheckThatRanIsRedAndOneThatNeverStartedIsNotRun(t *testing.T) {
	t.Parallel()

	w := newWorld()
	ready(w, red("1", "fail"))

	rows, err := poll(t, w)
	require.NoError(t, err)
	assert.Equal(t, []string{"CI-RED     #6 failed=1 not_started=0"}, lines(t, rows))

	w.notStarted[link+"1"] = true
	rows, err = poll(t, w)
	require.NoError(t, err)
	assert.Equal(t, []string{"CI-NOT-RUN #6 not_started=1"}, lines(t, rows))

	ready(w, red("1", "fail"), red("2", "fail"))
	rows, err = poll(t, w)
	require.NoError(t, err)
	assert.Equal(t, []string{"CI-RED     #6 failed=1 not_started=1"}, lines(t, rows))
}

func TestCI_OnlyAFailedCheckWithALinkCounts(t *testing.T) {
	t.Parallel()

	w := newWorld()
	ready(w, red("9", "pass"), model.Check{Name: "x", Bucket: "fail"})

	rows, err := poll(t, w)
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestCI_AFailedAnnotationReadCountsAsRan(t *testing.T) {
	t.Parallel()

	w := newWorld()
	ready(w, red("1", "fail"))
	w.fails["CheckNotStarted"] = true

	rows, err := poll(t, w)
	require.NoError(t, err)
	assert.Equal(t, []string{"CI-RED     #6 failed=1 not_started=0"}, lines(t, rows))
}

func TestCI_ADraftIsSkippedAndAFailedReadStopsThePoll(t *testing.T) {
	t.Parallel()

	w := newWorld()
	ready(w, red("1", "fail"))

	draft := true
	v := w.views[6]
	v.IsDraft = &draft
	w.views[6] = v

	rows, err := poll(t, w)
	require.NoError(t, err)
	assert.Empty(t, rows)

	ready(w, red("1", "fail"))
	w.fails["PRChecks"] = true
	_, err = poll(t, w)
	require.ErrorContains(t, err, "cannot read the checks of #6")
}

func TestCI_AFailedListOfOurPullRequestsStopsThePoll(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.fails["PRListOurs"] = true

	_, err := poll(t, w)
	require.ErrorIs(t, err, errDown)
	assert.Contains(t, err.Error(), "cannot list our ready pull requests")
}
