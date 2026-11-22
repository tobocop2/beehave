package takeover_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/takeover"
)

func comments(pairs ...string) []model.Comment {
	var rows []model.Comment

	for i := 0; i+1 < len(pairs); i += 2 {
		rows = append(rows, model.Comment{User: model.Account{Login: pairs[i]}, CreatedAt: pairs[i+1], Path: "a.go", Body: "line"})
	}

	return rows
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

func TestRead_APullRequestThatCannotBeReadIsARefusal(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.viewErr = errDown
	_, err := s.try(takeover.ModeDry)

	var refusal *takeover.Refusal

	require.ErrorAs(t, err, &refusal)
	require.ErrorIs(t, err, errDown)
	assert.Equal(t, "cannot read #7.", refusal.Message)
}

func TestRead_ReviewCommentsThatCannotBeReadAreARefusal(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.rowsErr = errDown
	_, err := s.try(takeover.ModeDry)

	var refusal *takeover.Refusal

	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "cannot read the review comments of #7.", refusal.Message)
}

func TestRead_AnAssigneeWithoutALoginIsAnEmptyName(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9})
	s.pr["assignees"] = []obj{login("a"), {}}

	assert.Contains(t, s.run(t, takeover.ModeDry).Lines[0], "assignees [a,]")
}
