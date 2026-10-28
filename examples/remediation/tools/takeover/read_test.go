package takeover_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/takeover"
	"github.com/tobocop2/obee/internal/check"
	"github.com/tobocop2/obee/internal/model"
)

const badTime = "2026-10-23T11:00:00"

func TestRead_ATimeThatIsNotAUTCStampIsRefusedInEverySource(t *testing.T) {
	t.Parallel()

	sources := map[string]func(*setup){
		"our notice": func(s *setup) {
			s.pr["comments"] = append(s.comments(), obj{"author": login("me"), "createdAt": badTime, "body": "Taking over this pull request"})
		},
		"a review": func(s *setup) {
			s.pr["reviews"] = []obj{{"author": login("x"), "state": "COMMENTED", "submittedAt": badTime, "body": "x"}}
		},
		"a review comment": func(s *setup) {
			s.rows = []model.Comment{{User: model.Account{Login: "x"}, CreatedAt: badTime, Path: "a.go", Body: "x"}}
		},
		"a commit": func(s *setup) {
			s.pr["commits"] = []obj{{"authors": []obj{login("x")}, "committedDate": badTime}}
		},
	}

	for name, change := range sources {
		s := newSetup(spec{age: 9})
		change(s)
		_, err := s.try(takeover.ModeDry)

		var refusal *check.Refusal

		require.ErrorAs(t, err, &refusal, name)
		assert.Equal(t, "cannot read the time '"+badTime+"' as a UTC timestamp.", refusal.Message, name)
	}
}

func TestRead_ACommitWithNoDateIsNoPush(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.pr["commits"] = []obj{{"authors": []obj{login("them")}, "committedDate": "soon"}}
	got := s.run(t, takeover.ModeDry)

	assert.Contains(t, got.Lines[0], "from 1 timestamps")
}

func TestRead_OurOwnCommentThatIsNoNoticeIsIgnored(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.pr["comments"] = append(s.comments(),
		obj{"author": login("me"), "createdAt": ago(1), "body": "thanks"},
		obj{"author": login("me"), "createdAt": nil, "body": "Taking over this pull request"})
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Contains(t, all(got), "no notice has been posted")
}

func TestRead_OurOwnCommentWithoutTextIsNoNoticeAndNoError(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.pr["comments"] = append(s.comments(),
		obj{"author": login("me"), "createdAt": ago(1), "body": nil},
		obj{"author": login("me"), "createdAt": ago(1)})
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeRule, got.Code)
	assert.Contains(t, all(got), "no notice has been posted")
}

func TestGuard_AStateThatIsMissingReadsAsNull(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.pr["state"] = nil
	got := s.run(t, takeover.ModeDry)

	assert.Equal(t, takeover.CodeGuard, got.Code)
	assert.Equal(t, "REFUSE: #7 is null, not OPEN", last(got))
}

func TestTake_AClaimThatCannotBeReadIsAnErrorAndNothingSaysTheItemMayBeTaken(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{assignees: []string{"me"}, age: 9, notice: 5})
	s.freeErr = errDown

	got, err := s.try(takeover.ModeTake)

	require.ErrorIs(t, err, errDown)
	assert.NotContains(t, all(got), "may be taken")
}

func TestTake_ARefusalOfAGuardOrARuleNeverReadsTheClaim(t *testing.T) {
	t.Parallel()

	own := newSetup(spec{age: 9, notice: 5, author: "me"})
	own.freeErr = errDown
	tooYoung := newSetup(spec{age: 9, notice: 0.2})
	tooYoung.freeErr = errDown

	for name, s := range map[string]*setup{"own pull request": own, "young notice": tooYoung} {
		got, err := s.try(takeover.ModeTake)

		require.NoError(t, err, name)
		assert.NotZero(t, got.Code, name)
		assert.Zero(t, s.count("free"), name)
	}
}

func TestTake_APushAfterTheNoticeRefusesBeforeAnyReplyBlockPrints(t *testing.T) {
	t.Parallel()

	commit := obj{"authors": []obj{login("them")}, "committedDate": ago(1), "messageHeadline": "wip"}

	for name, mutate := range map[string][]func(*takeover.Request){"unread": nil, "read": {readReplies}} {
		s := newSetup(spec{age: 9, notice: 5, reply: "one moment", replyAge: 3, commits: []obj{commit}})
		got := s.run(t, takeover.ModeTake, mutate...)

		assert.Equal(t, takeover.CodeRule, got.Code, name)
		assert.Contains(t, all(got), "somebody pushed 1 commit(s)", name)
		assert.NotContains(t, all(got), "replies after", name)
		assert.Zero(t, s.count("free"), name)
	}
}
