package takeover_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/takeover"
	"github.com/tobocop2/obee/internal/check"
)

func readReplies(r *takeover.Request) { r.ReplyRead = true }

func TestTake_AssignedNotIdleNoticeAgedOutStillRefuses(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 0.2, notice: 5})
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Contains(t, all(got), "Assignment does not shorten that wait")
	assert.Zero(t, s.count("free"))
}

func TestTake_AssignedIdleNoticeAgedOutNoReplyTakes(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9, notice: 5})
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, 0, got.Code)
	assert.Equal(t, []string{"free"}, s.calls)
	assert.Equal(t, "#7 may be taken now (basis: assigned) after a 5.00h grace. The run of the item holds the claim.", last(got))
	assert.NotContains(t, all(got), "TAKEN")
}

func TestTake_IdleButNoNoticeRefuses(t *testing.T) {
	t.Parallel()

	got := newSetup(spec{assignees: []string{"me"}, age: 9}).run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Contains(t, all(got), "no notice has been posted")
}

func TestTake_AKeepReplyStopsIt(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9, notice: 5, reply: "KEEP"})
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Contains(t, all(got), "says KEEP")
	assert.Zero(t, s.count("free"))
}

func TestTake_AReleaseReplyIsHonouredAlthoughTheReplyResetsIdleness(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9, notice: 5, reply: "RELEASE"})
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, 0, got.Code)
	assert.Contains(t, all(got), "RELEASE received on #7: taking it now, without the grace period or the idle wait.")
	assert.NotContains(t, all(got), "inside the")
	assert.Equal(t, 1, s.count("free"))
}

func TestTake_NoChangesRequestedReviewRefusesAndReleaseDoesNotExcuseIt(t *testing.T) {
	t.Parallel()

	got := newSetup(spec{age: 9, notice: 5, reply: "RELEASE", noReview: true}).run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Contains(t, all(got), "Post the review first")
	assert.NotContains(t, all(got), "RELEASE received")
}

func TestTake_APushAfterTheNoticeRefuses(t *testing.T) {
	t.Parallel()

	commit := obj{"authors": []obj{login("them")}, "committedDate": ago(1), "messageHeadline": "wip"}
	got := newSetup(spec{age: 9, notice: 5, commits: []obj{commit}}).run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Contains(t, all(got), "somebody pushed 1 commit(s) to #7 after the notice")
}

func TestTake_ACommitBeforeOrAtTheSecondOfTheNoticeIsNoPush(t *testing.T) {
	t.Parallel()

	for name, when := range map[string]float64{"before": 7, "same second": 5} {
		commit := obj{"authors": []obj{login("them")}, "committedDate": ago(when), "messageHeadline": "old"}
		s := newSetup(spec{age: 9, notice: 5, commits: []obj{commit}})
		got := s.run(t, takeover.ModeTake)

		assert.Equal(t, 0, got.Code, name)
		assert.Equal(t, 1, s.count("free"), name)
		assert.NotContains(t, all(got), "note:", name)
	}
}

func TestTake_AnAuthorWhoIsUsDoesNotPush(t *testing.T) {
	t.Parallel()

	commit := obj{"authors": []obj{login("me")}, "committedDate": ago(1)}

	assert.Equal(t, 0, newSetup(spec{age: 9, notice: 5, commits: []obj{commit}}).run(t, takeover.ModeTake).Code)
}

func TestTake_ACommitWithTwoAuthorsCountsForEachWhoIsNotUs(t *testing.T) {
	t.Parallel()

	commit := obj{"authors": []obj{login("me"), login("a"), login("b")}, "committedDate": ago(2)}
	got := newSetup(spec{age: 9, notice: 5, commits: []obj{commit}}).run(t, takeover.ModeTake)

	assert.Contains(t, all(got), "somebody pushed 2 commit(s)")
}

func TestTake_ACommitWithNoAuthorCountsOnceAsUnknown(t *testing.T) {
	t.Parallel()

	commit := obj{"authors": []obj{}, "committedDate": ago(2)}
	got := newSetup(spec{age: 9, notice: 5, commits: []obj{commit}}).run(t, takeover.ModeTake)

	assert.Contains(t, all(got), "somebody pushed 1 commit(s)")
}

func TestTake_TheAgeSitsOnTheIdleBoundary(t *testing.T) {
	t.Parallel()

	atBoundary := newSetup(spec{age: 1, notice: 5})
	assert.Equal(t, 0, atBoundary.run(t, takeover.ModeTake, readReplies).Code)

	inside := newSetup(spec{age: 0.99, notice: 5})
	assert.Equal(t, takeover.CodeRule, inside.run(t, takeover.ModeTake, readReplies).Code)
}

func TestDry_TheAgeIsRoundedToTwoDecimalsBeforeItIsCompared(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 1})
	s.pr["comments"] = []obj{{"author": login("them"), "createdAt": "2026-10-23T11:00:02Z", "body": "x"}}
	got := s.run(t, takeover.ModeDry)

	assert.Contains(t, all(got), "(1.00h ago")
	assert.Contains(t, all(got), "WOULD POST")
}

func TestTake_TheGracePeriodSitsOnItsBoundary(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, newSetup(spec{age: 9, notice: 1}).run(t, takeover.ModeTake).Code)

	refused := newSetup(spec{age: 9, notice: 0.99}).run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeRule, refused.Code)
	assert.Contains(t, all(refused), "the notice is only 0.99h old and the grace period is 1h")
}

func TestTake_AReplyAtTheSecondOfTheNoticeIsNotNewer(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5})
	s.pr["comments"] = append(s.comments(), obj{"author": login("them"), "createdAt": ago(5), "body": "hi"})
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, 0, got.Code)
	assert.NotContains(t, all(got), "note:")
}

func TestTake_TheNewestNoticeCounts(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9, notice: 5})
	s.pr["comments"] = append(s.comments(), obj{"author": login("me"), "createdAt": ago(8), "body": "Taking over this pull request: x"})

	assert.Contains(t, s.run(t, takeover.ModeDry).Lines, "notice already posted at "+ago(5))
}

func TestTake_ANoticeLooksOnlyAtOurOwnComments(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.pr["comments"] = append(s.comments(), obj{"author": login("them"), "createdAt": ago(5), "body": "Taking over this pull request: x"})

	assert.NotContains(t, all(s.run(t, takeover.ModeDry)), "notice already posted")
}

func TestPlan_StampsComeFromCommitsCommentsReviewsAndReviewComments(t *testing.T) {
	t.Parallel()

	commit := obj{"authors": []obj{login("them")}, "committedDate": "2026-01-01T00:00:00Z"}
	s := newSetup(spec{age: 9, commits: []obj{commit}})
	s.pr["reviews"] = []obj{{"author": login("x"), "submittedAt": "2026-02-01T00:00:00Z"}, {"author": login("me"), "state": "CHANGES_REQUESTED", "submittedAt": "2020-01-01T00:00:00Z"}}
	s.rows = comments("y", "2026-03-01T00:00:00Z", "me", "2026-04-01T00:00:00Z")

	assert.Contains(t, s.run(t, takeover.ModeDry).Lines[0], "from 4 timestamps")
}

func TestPlan_ADraftReviewWithNoTimeIsNotATimestamp(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.pr["reviews"] = []obj{{"author": login("x"), "submittedAt": nil}, {"author": login("me"), "state": "CHANGES_REQUESTED", "submittedAt": nil}}

	assert.Contains(t, s.run(t, takeover.ModeDry).Lines[0], "from 1 timestamps")
}

func TestPlan_NoTimestampAtAllIsARefusalNotIdle(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.pr["comments"] = []obj{}
	got := s.run(t, takeover.ModeDry)

	assert.Equal(t, takeover.CodeGuard, got.Code)
	assert.Contains(t, all(got), "produced no activity timestamps at all")
}

func TestPlan_ATimeThatIsNotAUTCStampIsRefused(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"2026-10-23T11:00:00", "2026-soon"} {
		s := newSetup(spec{age: 9})
		s.pr["comments"] = []obj{{"author": login("them"), "createdAt": text, "body": "x"}}
		_, err := s.try(takeover.ModeDry)

		var refusal *check.Refusal

		require.ErrorAs(t, err, &refusal, text)
		assert.Equal(t, "cannot read the time '"+text+"' as a UTC timestamp.", refusal.Message)
	}
}

func TestPlan_AValueThatIsNotADateIsNotATimestamp(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.pr["comments"] = append(s.comments(), obj{"author": login("them"), "createdAt": "soon", "body": "x"})

	assert.Contains(t, s.run(t, takeover.ModeDry).Lines[0], "from 1 timestamps")
}

func TestPlan_TheWindowOfIdleIsTheSettingOfTheRound(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.idle = 10 * time.Hour

	assert.Contains(t, all(s.run(t, takeover.ModeDry)), "WOULD DO NOTHING")
}
