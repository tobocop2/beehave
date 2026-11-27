package takeover_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc/proctest"
	"github.com/tobocop2/beehest/internal/takeover"
)

var viewCall = []string{"pr", "view", "7", "--json", "url,closingIssuesReferences"}

func TestTake_TheTakeAssignsThePullRequestAndEveryLinkedIssue(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5})
	s.gh = listing("https://github.com/o/n/issues/12", "https://github.com/x/y/issues/99", "https://github.com/o/n/issues/15")
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, 0, got.Code)
	assert.Equal(t, [][]string{viewCall, edit("pr", "7"), edit("issue", "12"), edit("issue", "15")}, s.gh.Calls)
	assert.Contains(t, all(got), "assigned #7 and the issues 12, 15 to me.")
	assert.Contains(t, last(got), "#7 may be taken now")
}

func TestTake_APullRequestThatClosesNoIssueIsAssignedAlone(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5})
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, 0, got.Code)
	assert.Equal(t, [][]string{viewCall, edit("pr", "7")}, s.gh.Calls)
	assert.Contains(t, all(got), "assigned #7 to me.")
}

func TestTake_ARefusedTakeAndTheOtherStepsRunNoGh(t *testing.T) {
	t.Parallel()

	held := newSetup(spec{age: 9, notice: 5})
	held.freeErr = errDown

	cases := map[string]struct {
		s    *setup
		mode takeover.Mode
	}{
		"the notice is young":  {newSetup(spec{age: 9, notice: 0.2}), takeover.ModeTake},
		"the claim is held":    {held, takeover.ModeTake},
		"a reply says KEEP":    {newSetup(spec{age: 9, notice: 5, reply: "KEEP", replyAge: 1}), takeover.ModeTake},
		"inside the idle wait": {newSetup(spec{age: 0.2, notice: 5}), takeover.ModeTake},
		"the dry read":         {newSetup(spec{age: 9, notice: 5}), takeover.ModeDry},
		"the apply":            {newSetup(spec{age: 9}), takeover.ModeApply},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := c.s.try(c.mode)

			assert.Empty(t, c.s.gh.Calls, "result %v, error %v", got, err)
		})
	}
}

func TestTake_AFailedReadOfTheClosingIssuesAssignsNothing(t *testing.T) {
	t.Parallel()

	answers := map[string]model.CommandResult{
		"gh fails":     {ExitCode: 1, Stderr: "no access\n"},
		"not json":     {Stdout: "not json"},
		"null payload": {Stdout: "null"},
	}

	for name, answer := range answers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s := newSetup(spec{age: 9, notice: 5})
			s.gh = proctest.Always(answer)
			_, err := s.try(takeover.ModeTake)

			require.ErrorContains(t, err, "cannot read the issues that #7 closes")
			assert.Len(t, s.gh.Calls, 1)
		})
	}
}

func TestTake_AFailedAssignOfThePullRequestIsAnErrorAndAssignsNoIssue(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5})
	s.gh = listing("https://github.com/o/n/issues/12")
	s.gh.PlanFail("no access\n", "pr", "edit")
	_, err := s.try(takeover.ModeTake)

	require.ErrorContains(t, err, "cannot assign pr 7 to me.")
	require.ErrorContains(t, err, "gh pr edit 7: no access")
	assert.Equal(t, [][]string{viewCall, edit("pr", "7")}, s.gh.Calls)
}

func TestTake_AFailedAssignOfAnIssueIsAnErrorAndStopsThere(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5})
	s.gh = listing("https://github.com/o/n/issues/12", "https://github.com/o/n/issues/15")
	s.gh.PlanFail("no access\n", "issue", "edit", "12")
	_, err := s.try(takeover.ModeTake)

	require.ErrorContains(t, err, "cannot assign issue 12 to me.")
	assert.Equal(t, [][]string{viewCall, edit("pr", "7"), edit("issue", "12")}, s.gh.Calls)
}

func TestTake_AHeadThatMovesAfterThePlanRefusesAndAssignsNothing(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5})
	s.gh = listing("https://github.com/o/n/issues/12")
	s.beforeSecondView = func() { s.pr["headRefOid"] = pushed }
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeGuard, got.Code)
	assert.Contains(t, all(got), "REFUSE: #7 moved from "+planned+" to "+pushed+" since the plan. Nothing assigned.")
	assert.Equal(t, [][]string{viewCall}, s.gh.Calls)
}

func TestTake_APullRequestClosedAfterThePlanRefusesAndAssignsNothing(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5})
	s.beforeSecondView = func() { s.pr["state"] = "CLOSED" }
	got := s.run(t, takeover.ModeTake)

	assert.Equal(t, takeover.CodeGuard, got.Code)
	assert.Contains(t, all(got), "#7 is CLOSED, not OPEN")
	assert.Equal(t, [][]string{viewCall}, s.gh.Calls)
}

func TestTake_AReadThatFailsAfterThePlanAssignsNothing(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5})
	s.secondViewErr = errDown
	_, err := s.try(takeover.ModeTake)

	require.ErrorIs(t, err, errDown)
	assert.Equal(t, [][]string{viewCall}, s.gh.Calls)
}

func TestTake_TheReReadAsksForTheStateTheAuthorAndTheHead(t *testing.T) {
	t.Parallel()

	s := newSetup(spec{age: 9, notice: 5})
	s.run(t, takeover.ModeTake)

	require.Len(t, s.fields, 2)
	assert.ElementsMatch(t, []string{"state", "author", "headRefOid"}, s.fields[1])
}
