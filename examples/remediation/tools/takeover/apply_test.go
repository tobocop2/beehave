package takeover_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/examples/remediation/tools/check"
	"github.com/tobocop2/beehave/examples/remediation/tools/takeover"
)

func TestDry_AssignedButNotIdleTheNoticeMayGoOut(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 0.2})
	got := s.run(t, takeover.ModeDry)

	assert.Equal(t, 0, got.Code)
	assert.Contains(t, all(got), "WOULD POST")
	assert.Empty(t, s.posted)
}

func TestDry_UnassignedAndNotIdleNothingIsPosted(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 0.2})
	got := s.run(t, takeover.ModeDry)

	assert.Equal(t, 0, got.Code)
	assert.Contains(t, all(got), "WOULD DO NOTHING: not assigned to you, and touched 0.20h ago")
	assert.Empty(t, s.posted)
}

func TestDry_NoChangesRequestedReviewSaysSo(t *testing.T) {
	t.Parallel()

	got := newSetup(spec{age: 9, noReview: true}).run(t, takeover.ModeDry)

	assert.Contains(t, all(got), "WOULD DO NOTHING: no changes-requested review of yours is on #7")
}

func TestDry_DoesNotReadTheClaim(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})

	assert.Equal(t, 0, s.run(t, takeover.ModeDry).Code)
	assert.Zero(t, s.count("free"))
}

func TestApply_WithTheReviewTheNoticeIsPosted(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	got := s.run(t, takeover.ModeApply)

	assert.Equal(t, 0, got.Code)
	require.Len(t, s.posted, 1)
	assert.Contains(t, s.posted[0], "it has had no activity for 9.00h, past the 1h I wait")
	assert.Contains(t, s.posted[0], "Reply **KEEP**")
	assert.Contains(t, s.posted[0], "Reply **RELEASE** and I will start now instead of waiting.")
	assert.Contains(t, s.posted[0], "Anything else, or no reply within 1h, and I will take it over.")
	assert.Contains(t, last(got), "notice posted on #7 (basis: idle).")
}

func TestApply_TheAssignedNoticeSaysWhy(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 0.2})
	s.run(t, takeover.ModeApply)

	require.Len(t, s.posted, 1)
	assert.Contains(t, s.posted[0], "it is assigned to me and was last updated 0.20h ago, so I expect to pick up the remaining work")
}

func TestApply_AFractionOfAnHourIsWrittenWithoutZeros(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.idle, s.grace = 90*60*1e9, 30*60*1e9
	s.run(t, takeover.ModeApply)

	assert.Contains(t, s.posted[0], "past the 1.5h I wait")
	assert.Contains(t, s.posted[0], "no reply within 0.5h")
}

func TestApply_NoChangesRequestedReviewPostsNothing(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, noReview: true})
	got := s.run(t, takeover.ModeApply)

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Empty(t, s.posted)
}

func TestApply_ANoticeTheForgeAlreadyHoldsIsUsedAndNotPostedAgain(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9, notice: 5})
	got := s.run(t, takeover.ModeApply)

	assert.Equal(t, 0, got.Code)
	assert.Contains(t, all(got), "notice already posted on #7")
	assert.Empty(t, s.posted)
}

func TestApply_ANoticeThatLandsAfterTheFirstReadIsNotPostedAgain(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9})
	s.beforeSecondView = func() {
		s.pr["comments"] = append(s.comments(), obj{"author": login("me"), "createdAt": ago(0.1), "body": "Taking over this pull request: it is assigned to me"})
	}
	got := s.run(t, takeover.ModeApply)

	assert.Equal(t, 0, got.Code)
	assert.Contains(t, all(got), "notice already posted on #7")
	assert.Empty(t, s.posted)
	assert.Equal(t, 2, s.views)
}

func TestApply_ANoticeWithAnUnreadableTimeBeforeThePostPostsNothing(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9})
	s.beforeSecondView = func() {
		s.pr["comments"] = append(s.comments(), obj{"author": login("me"), "createdAt": "2026-13-45", "body": "Taking over this pull request: it is assigned to me"})
	}
	_, err := s.try(takeover.ModeApply)

	require.Error(t, err)
	assert.Empty(t, s.posted)
}

func TestApply_AFailedReadOfTheCommentsBeforeThePostPostsNothing(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9})
	s.secondViewErr = errDown
	_, err := s.try(takeover.ModeApply)

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	require.ErrorIs(t, err, errDown)
	assert.Equal(t, "cannot read the comments of #7.", refusal.Message)
	assert.Empty(t, s.posted)
}

func TestApply_RefusesInsideTheThresholdWhenUnassigned(t *testing.T) {
	t.Parallel()

	got := newSetup(spec{age: 0.2}).run(t, takeover.ModeApply)

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Contains(t, all(got), "inside the 1h threshold. Nothing posted.")
}

func TestApply_ReadsTheClaimBeforeItPosts(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9})

	assert.Equal(t, 0, s.run(t, takeover.ModeApply).Code)
	assert.Equal(t, []string{"free", "post"}, s.calls)
}

func TestApply_PostsNothingWhenTheClaimIsHeldOrUnreadable(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9})
	s.freeErr = errDown

	_, err := s.try(takeover.ModeApply)

	require.ErrorIs(t, err, errDown)
	assert.Empty(t, s.posted)
}

func TestApply_AFailedCommentIsARefusal(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9})
	s.postErr = errDown
	_, err := s.try(takeover.ModeApply)

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	require.ErrorIs(t, err, errDown)
	assert.Equal(t, "cannot post the notice on #7.", refusal.Message)
}

func TestApply_APullRequestClosedAfterTheFirstReadGetsNoNotice(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9})
	s.beforeSecondView = func() { s.pr["state"] = "CLOSED" }
	got := s.run(t, takeover.ModeApply)

	assert.Equal(t, takeover.CodeGuard, got.Code)
	assert.Contains(t, all(got), "#7 is CLOSED, not OPEN")
	assert.Empty(t, s.posted)
}

func TestApply_TheReReadAsksForTheCommentsTheStateTheAuthorAndTheHead(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9})
	s.run(t, takeover.ModeApply)

	require.Len(t, s.fields, 2)
	assert.ElementsMatch(t, []string{"comments", "state", "author", "headRefOid"}, s.fields[1])
}

func TestApply_AHeadThatMovesAfterTheFirstReadGetsNoNotice(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9})
	s.beforeSecondView = func() { s.pr["headRefOid"] = "bbb" }
	got := s.run(t, takeover.ModeApply, head("aaa"))

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Contains(t, all(got), "The author pushed")
	assert.Empty(t, s.posted)
}
