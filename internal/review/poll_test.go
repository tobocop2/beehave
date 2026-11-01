package review_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/review"
)

func onePull(w *world, reviews ...model.Review) {
	w.lists[requested] = []int{1}
	w.views[1] = pullView(1, nil)
	w.reviews[1] = reviews
}

func poll(t *testing.T, w *world) ([]review.Row, error) {
	t.Helper()

	return review.Poll(context.Background(), w, w, me, today)
}

func kindLine(t *testing.T, w *world) string {
	t.Helper()

	rows, err := poll(t, w)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	return rows[0].String()
}

func TestPoll_APullRequestWeNeverReviewedIsNew(t *testing.T) {
	t.Parallel()

	w := newWorld()
	onePull(w)

	want := "NEW        #1 alice head=aaaaaaaaa ours=NONE@- draft=false upd=2026-10-20T10:00 " + pullView(1, nil).Title[:60]
	assert.Equal(t, want, kindLine(t, w))
}

func TestPoll_TheKindFollowsOurLastReviewAndTheHead(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		state  model.ReviewState
		commit string
		want   string
	}{
		{model.ReviewApproved, head, "CURRENT"},
		{model.ReviewChangesRequested, head, "WAITING"},
		{model.ReviewApproved, old, "RE-REVIEW"},
		{model.ReviewChangesRequested, old, "FOLLOW-UP"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			onePull(w, review1(me, tc.state, tc.commit, "2026-10-19T00:00:00Z"))
			assert.Equal(t, tc.want, kindLine(t, w)[:len(tc.want)])
		})
	}
}

func TestPoll_ACommentedOrDismissedOrPendingReviewIsNoVerdict(t *testing.T) {
	t.Parallel()

	for _, state := range []model.ReviewState{model.ReviewCommented, model.ReviewDismissed, model.ReviewPending} {
		w := newWorld()
		onePull(w, review1(me, state, head, "2026-10-19T00:00:00Z"))
		assert.Contains(t, kindLine(t, w), "NEW")
	}
}

func TestPoll_ALineShowsOurReviewCommitAndAReply(t *testing.T) {
	t.Parallel()

	w := newWorld()
	onePull(w, review1(me, model.ReviewApproved, head, "2026-10-19T00:00:00Z"))
	w.issueComments[1] = []model.Comment{comment("alice", "2026-10-20T00:00:00Z")}

	assert.Equal(t, "REPLIED    #1 alice head=aaaaaaaaa ours=APPROVED@aaaaaaaaa draft=false upd=2026-10-20T10:00 "+
		pullView(1, nil).Title[:60]+" reply=2026-10-20T00:00:00Z", kindLine(t, w))
}

func TestPoll_WhoDoesNotCountAsAReply(t *testing.T) {
	t.Parallel()

	for name, other := range map[string]model.Comment{
		"we wrote it":   comment(me, "2026-10-20T00:00:00Z"),
		"a bot marker":  comment("renovate[bot]", "2026-10-20T00:00:00Z"),
		"a known bot":   comment("github-actions", "2026-10-20T00:00:00Z"),
		"before ours":   comment("alice", "2026-10-18T00:00:00Z"),
		"at the moment": comment("alice", "2026-10-19T00:00:00Z"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			onePull(w, review1(me, model.ReviewApproved, head, "2026-10-19T00:00:00Z"))
			w.issueComments[1] = []model.Comment{other}
			assert.Contains(t, kindLine(t, w), "CURRENT")
		})
	}
}

func TestPoll_AReviewCommentOrAReviewByOthersCountsAsAReply(t *testing.T) {
	t.Parallel()

	w := newWorld()
	onePull(w, review1(me, model.ReviewApproved, head, "2026-10-19T00:00:00Z"))
	w.reviewComments[1] = []model.Comment{comment("alice", "2026-10-20T00:00:00Z")}
	assert.Contains(t, kindLine(t, w), "REPLIED")

	w = newWorld()
	onePull(w, review1(me, model.ReviewApproved, head, "2026-10-19T00:00:00Z"),
		review1("bob", model.ReviewApproved, head, "2026-10-20T00:00:00Z"),
		model.Review{User: model.Account{Login: "bob"}, State: model.ReviewCommented})
	assert.Contains(t, kindLine(t, w), "CURRENT")

	w.reviews[1] = append(w.reviews[1], review1("bob", model.ReviewChangesRequested, head, "2026-10-20T01:00:00Z"))
	assert.Contains(t, kindLine(t, w), "REPLIED")
}

func TestPoll_AReviewRequestOnACurrentPullRequestIsAReReview(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		asked string
		want  string
	}{
		"nobody asked":         {`[]`, "CURRENT"},
		"another user asked":   {`[{"login":"bob"}]`, "CURRENT"},
		"we are asked":         {`[{"login":"me"}]`, "RE-REVIEW"},
		"a team of our name":   {`[{"name":"me"}]`, "RE-REVIEW"},
		"an empty request row": {`[{}]`, "CURRENT"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			onePull(w, review1(me, model.ReviewApproved, head, "2026-10-19T00:00:00Z"))
			v := w.views[1]
			require.NoError(t, json.Unmarshal([]byte(tc.asked), &v.ReviewRequests))
			w.views[1] = v

			assert.Contains(t, kindLine(t, w), tc.want)
		})
	}
}

func TestPoll_AnUnknownHeadAndDraftPrintAsNull(t *testing.T) {
	t.Parallel()

	w := newWorld()
	onePull(w)
	v := w.views[1]
	v.HeadRefOid, v.IsDraft = nil, nil
	w.views[1] = v

	assert.Contains(t, kindLine(t, w), "head=null ours=NONE@- draft=null")
}

func TestPoll_AFailedCommentReadOnACurrentPullRequestStopsThePoll(t *testing.T) {
	t.Parallel()

	w := newWorld()
	onePull(w, review1(me, model.ReviewApproved, head, "2026-10-19T00:00:00Z"))
	w.fails["Comments"] = true

	_, err := poll(t, w)
	require.ErrorIs(t, err, errDown)
	assert.Contains(t, err.Error(), "cannot read the comments of #1")
}

func TestPoll_TheJSONHoldsTheRowsAsTheyPrint(t *testing.T) {
	t.Parallel()

	w := newWorld()
	onePull(w)

	rows, err := poll(t, w)
	require.NoError(t, err)

	data, err := json.Marshal(rows)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"kind":"NEW","number":1,"author":"alice","head":"`+head+`","ours":"NONE","ours_at":"-","isDraft":false,`+
		`"updatedAt":"2026-10-20T10:00:00Z","title":"`+pullView(1, nil).Title+`"}]`, string(data))
}

func TestPoll_TheRowIsALedgerOfOurLastReviewItsCommitAndWhetherThatCommitIsStillTheHead(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		commit string
		still  bool
	}{{head, true}, {old, false}} {
		w := newWorld()
		onePull(w, review1(me, model.ReviewApproved, tc.commit, "2026-10-19T00:00:00Z"))

		rows, err := poll(t, w)
		require.NoError(t, err)
		require.Len(t, rows, 1)

		data, err := json.Marshal(rows[0])
		require.NoError(t, err)

		var got map[string]any
		require.NoError(t, json.Unmarshal(data, &got))
		assert.Equal(t, "APPROVED", got["ours"])
		assert.Equal(t, tc.commit, got["ours_at"])
		assert.Equal(t, "2026-10-19T00:00:00Z", got["ours_when"])
		assert.Equal(t, tc.still, got["still_head"], tc.commit)
	}
}

func TestPoll_ARowWithNoReviewOfOursHasNoLedgerFields(t *testing.T) {
	t.Parallel()

	w := newWorld()
	onePull(w)

	rows, err := poll(t, w)
	require.NoError(t, err)

	data, err := json.Marshal(rows[0])
	require.NoError(t, err)
	assert.NotContains(t, string(data), "still_head")
	assert.NotContains(t, string(data), "ours_when")
}

func TestPoll_NothingToDoGivesNoRows(t *testing.T) {
	t.Parallel()

	rows, err := poll(t, newWorld())
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestPoll_TheThreeListingsAreMergedWithoutDuplicatesAndSorted(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.lists[requested] = []int{3, 1}
	w.lists["--state open --search reviewed-by:me -author:me --json number"] = []int{1, 2}
	w.lists["--state open --search -author:me -author:app/dependabot --json number"] = []int{2}

	for n := 1; n <= 3; n++ {
		w.views[n] = pullView(n, nil)
	}

	rows, err := poll(t, w)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, []int{rows[0].Number, rows[1].Number, rows[2].Number})
}

func TestPoll_AFailedReadStopsWithAnUnreadError(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"PRList", "PRView", "Reviews"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			w := newWorld()
			onePull(w)
			w.fails[name] = true

			_, err := poll(t, w)
			require.Error(t, err)

			var unread *review.UnreadError

			require.ErrorAs(t, err, &unread)
			require.ErrorIs(t, err, errDown)
			assert.Contains(t, err.Error(), "review-poll: ")
		})
	}
}

func TestPoll_TheSearchAsksForTheLastThreeDays(t *testing.T) {
	t.Parallel()

	w := newWorld()
	_, err := poll(t, w)
	require.NoError(t, err)
	assert.Equal(t, []model.SearchRequest{
		{Kind: model.SearchIssues, Involves: me, Since: "2026-10-17", Limit: review.SearchLimit},
		{Kind: model.SearchPulls, Involves: me, Since: "2026-10-17", Limit: review.SearchLimit},
	}, w.asked)
}
