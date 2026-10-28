package takeover_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/examples/remediation/tools/check"
	"github.com/tobocop2/obee/examples/remediation/tools/takeover"
	"github.com/tobocop2/obee/internal/model"
)

func comments(pairs ...string) []model.Comment {
	var rows []model.Comment

	for i := 0; i+1 < len(pairs); i += 2 {
		rows = append(rows, model.Comment{User: model.Account{Login: pairs[i]}, CreatedAt: pairs[i+1], Path: "a.go", Body: "line"})
	}

	return rows
}

func head(want string) func(*takeover.Request) {
	return func(r *takeover.Request) { r.HeadWant = want }
}

func TestGuard_OurOwnPullRequestIsRefusedOutright(t *testing.T) {
	t.Parallel()

	got := newSetup(spec{age: 9, author: "me"}).run(t, takeover.ModeDry)

	assert.Equal(t, takeover.CodeGuard, got.Code)
	assert.Contains(t, all(got), "is yours")
}

func TestGuard_APullRequestThatIsNotOpenIsRefusedBeforeTheCommentsAreRead(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"CLOSED", "MERGED"} {
		s := newSetup(spec{age: 9})
		s.pr["state"] = state
		s.rowsErr = errDown
		got := s.run(t, takeover.ModeDry)

		assert.Equal(t, takeover.CodeGuard, got.Code)
		assert.Equal(t, []string{"REFUSE: #7 is " + state + ", not OPEN"}, got.Lines)
		assert.NotContains(t, s.asked, "rows")
	}
}

func TestGuard_AMovedHeadRefusesInEveryModeBeforeTheCommentsAreRead(t *testing.T) {
	t.Parallel()

	for _, mode := range []takeover.Mode{takeover.ModeDry, takeover.ModeApply, takeover.ModeTake} {
		s := newSetup(spec{assignees: []string{"me"}, age: 9, notice: 5, reply: "RELEASE"})
		s.rowsErr = errDown
		got := s.run(t, mode, head("bbb"))

		assert.Equal(t, takeover.CodeRule, got.Code, mode)
		assert.Contains(t, all(got), "The author pushed", mode)
		assert.NotContains(t, all(got), "RELEASE received", mode)
		assert.NotContains(t, s.asked, "rows", mode)
	}
}

func TestGuard_TheSameHeadChangesNothing(t *testing.T) {
	t.Parallel()

	got := newSetup(spec{age: 9, notice: 5, reply: "RELEASE"}).run(t, takeover.ModeTake, head("aaa"))

	assert.Contains(t, all(got), "RELEASE received on #7")
}

func TestGuard_AnUnreadableForgeHeadIsRefused(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]any{"missing": nil, "empty": ""} {
		s := newSetup(spec{age: 9, notice: 5})
		if value == nil {
			delete(s.pr, "headRefOid")
		} else {
			s.pr["headRefOid"] = value
		}

		got := s.run(t, takeover.ModeTake, head("aaa"))

		assert.Equal(t, takeover.CodeGuard, got.Code, name)
		assert.Equal(t, []string{"REFUSE: cannot read the head of #7 from the forge."}, got.Lines, name)
	}
}

func TestRead_APullRequestThatCannotBeReadIsARefusal(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.viewErr = errDown
	_, err := s.try(takeover.ModeDry)

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	require.ErrorIs(t, err, errDown)
	assert.Equal(t, "cannot read #7.", refusal.Message)
}

func TestRead_ReviewCommentsThatCannotBeReadAreARefusal(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.rowsErr = errDown
	_, err := s.try(takeover.ModeDry)

	var refusal *check.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "cannot read the review comments of #7.", refusal.Message)
}

func TestRead_AnAssigneeWithoutALoginIsAnEmptyName(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.pr["assignees"] = []obj{login("a"), {}}

	assert.Contains(t, s.run(t, takeover.ModeDry).Lines[0], "assignees [a,]")
}
