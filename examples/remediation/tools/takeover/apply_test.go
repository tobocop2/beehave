package takeover_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/check"
	"github.com/tobocop2/obee/examples/remediation/tools/takeover"
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

func TestApply_ASecondNoticeIsNagging(t *testing.T) {
	t.Parallel()

	got := newSetup(spec{assignees: []string{"me"}, age: 9, notice: 5}).run(t, takeover.ModeApply)

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Contains(t, all(got), "Posting a second one is nagging")
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
