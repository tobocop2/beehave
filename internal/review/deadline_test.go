package review_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/review"
)

const reviewedAt = "2026-10-19T12:00:00Z"

// followUp is a world with one FOLLOW-UP row: we asked for changes at the time, and the head moved.
func followUp(reviewTime *string) *world {
	w := newWorld()
	w.lists[requested] = []int{1}
	w.views[1] = pullView(1, nil)
	w.reviews[1] = []model.Review{{User: model.Account{Login: me}, State: model.ReviewChangesRequested, CommitID: old, SubmittedAt: reviewTime}}

	return w
}

func pollAt(t *testing.T, w *world, now time.Time) ([]review.Row, error) {
	t.Helper()

	return review.Poll(context.Background(), w, w, me, now, lookbackDays)
}

func TestPoll_AFollowUpRowIsDueAtReviewTimePlus24HoursAndFailsClosedOnAnUnreadableTime(t *testing.T) {
	t.Parallel()

	reviewed, err := time.Parse(time.RFC3339, reviewedAt)
	require.NoError(t, err)

	for name, tc := range map[string]struct {
		now time.Time
		due bool
	}{
		"23h59_is_not_due":   {reviewed.Add(24*time.Hour - time.Minute), false},
		"24h00_is_due":       {reviewed.Add(24 * time.Hour), true},
		"24h01_is_due":       {reviewed.Add(24*time.Hour + time.Minute), true},
		"the_review_instant": {reviewed, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rows, err := pollAt(t, followUp(str(reviewedAt)), tc.now)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.NotNil(t, rows[0].Due)
			assert.Equal(t, tc.due, *rows[0].Due)
			assert.Equal(t, "2026-10-20T12:00:00Z", rows[0].Deadline)

			data, err := json.Marshal(rows[0])
			require.NoError(t, err)

			var got map[string]any

			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, "2026-10-20T12:00:00Z", got["deadline"])
			assert.Equal(t, tc.due, got["due"])
			assert.Contains(t, rows[0].String(), "deadline=2026-10-20T12:00:00Z due="+map[bool]string{true: "true", false: "false"}[tc.due])
		})
	}

	for name, reviewTime := range map[string]*string{"missing": nil, "empty": str(""), "not_a_time": str("yesterday")} {
		t.Run("unreadable_"+name, func(t *testing.T) {
			t.Parallel()

			rows, err := pollAt(t, followUp(reviewTime), today)
			require.Error(t, err)
			assert.Empty(t, rows, "an unreadable time gives no row, never a zero-age row")

			var unread *review.UnreadError

			require.ErrorAs(t, err, &unread)
			assert.Contains(t, err.Error(), "cannot read the time of our review of #1")
		})
	}
}

func TestPoll_OnlyAFollowUpRowCarriesADeadline(t *testing.T) {
	t.Parallel()

	w := newWorld()
	onePull(w, review1(me, model.ReviewApproved, head, reviewedAt))

	rows, err := poll(t, w)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].Due)
	assert.Empty(t, rows[0].Deadline)
	assert.NotContains(t, rows[0].String(), "deadline=")
}

// draftPull is a world with one draft pull request of alice that the logins marked ready, in order.
func draftPull(marks ...string) *world {
	w := newWorld()
	w.lists[requested] = []int{1}
	w.views[1] = pullView(1, func(p *model.PullRequest) { d := true; p.IsDraft = &d })
	w.ready[1] = marks

	return w
}

func TestPoll_ADraftMarkedReadyASecondTimeIsLetGo(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		marks []string
		count int
		let   bool
	}{
		"no_mark":                       {nil, 0, false},
		"one_mark_by_a_reviewer":        {[]string{"bob"}, 1, false},
		"two_marks_by_a_reviewer":       {[]string{"bob", "bob"}, 2, true},
		"two_reviewers":                 {[]string{"bob", "carol"}, 2, true},
		"the_author_marks_do_not_count": {[]string{"Alice", "bob", "alice"}, 1, false},
		"a_gone_account_does_not_count": {[]string{"bob", ""}, 1, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rows, err := poll(t, draftPull(tc.marks...))
			require.NoError(t, err)
			require.Len(t, rows, 1)
			assert.Equal(t, tc.count, rows[0].ReadyMarks)
			assert.Equal(t, tc.let, rows[0].LetGo)

			if tc.count > 0 {
				assert.Contains(t, rows[0].String(), " ready_marks="+strconv.Itoa(tc.count)+" let_go="+map[bool]string{true: "true", false: "false"}[tc.let])
			} else {
				assert.NotContains(t, rows[0].String(), "ready_marks")
			}
		})
	}
}

func TestPoll_AFailedReadOfTheReadyMarksOfADraftStopsThePoll(t *testing.T) {
	t.Parallel()

	w := draftPull("bob", "bob")
	w.fails["ReadyMarks"] = true

	_, err := poll(t, w)

	var unread *review.UnreadError

	require.ErrorAs(t, err, &unread)
	require.ErrorIs(t, err, errDown)
	assert.Contains(t, err.Error(), "cannot read the ready marks of #1")
}

func TestPoll_AReadyPullRequestReadsNoReadyMarks(t *testing.T) {
	t.Parallel()

	w := newWorld()
	onePull(w)
	w.fails["ReadyMarks"] = true
	w.ready[1] = []string{"bob", "bob"}

	rows, err := poll(t, w)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Zero(t, rows[0].ReadyMarks)
	assert.False(t, rows[0].LetGo)
}
