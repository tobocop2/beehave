package github_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/forge/forgetest"
)

func TestIssueAssignees_ReadsTheLoginsOnTheRepositoryOfTheForgeOrTheOneNamed(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "alice\nbob\n")

	logins, err := f.IssueAssignees(context.Background(), "", 5)
	require.NoError(t, err)
	assert.Equal(t, []string{"alice", "bob"}, logins)
	assert.True(t, run.Called("issue", "view", "5", "-R", repo))

	_, err = f.IssueAssignees(context.Background(), "acme/tools", 9)
	require.NoError(t, err)
	assert.True(t, run.Called("issue", "view", "9", "-R", "acme/tools"))

	none, _ := fixed(t, "")
	logins, err = none.IssueAssignees(context.Background(), "", 5)
	require.NoError(t, err)
	assert.Empty(t, logins)
}

func TestOptionalReads_ConformToTheFailedReadRules(t *testing.T) {
	t.Parallel()

	f, _ := failing(t, "boom")

	forgetest.CheckAssigneeReadsFail(t, f)
	forgetest.CheckReadyReadsFail(t, f)
}

func TestIssueAssignees_AFailedReadIsAnErrorNotAnEmptyList(t *testing.T) {
	t.Parallel()

	f, _ := failing(t, "boom")

	logins, err := f.IssueAssignees(context.Background(), "acme/tools", 9)

	require.ErrorContains(t, err, "cannot read the assignees of acme/tools#9")
	assert.Nil(t, logins)
}

func TestReadyMarks_ListsTheActorOfEachReadyForReviewEventInOrder(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `[[{"event":"commented","actor":{"login":"x"}},{"event":"ready_for_review","actor":{"login":"bob"}}],`+
		`[{"event":"convert_to_draft","actor":{"login":"alice"}},{"event":"ready_for_review","actor":null},{"event":"ready_for_review","actor":{"login":"carol"}}]]`)

	marks, err := f.ReadyMarks(context.Background(), 7)

	require.NoError(t, err)
	assert.Equal(t, []string{"bob", "", "carol"}, marks)
	assert.True(t, run.CalledContaining("repos/"+repo+"/issues/7/timeline"))
}

func TestReadyMarks_AFailedReadIsAnErrorNotAnEmptyList(t *testing.T) {
	t.Parallel()

	f, _ := failing(t, "boom")

	marks, err := f.ReadyMarks(context.Background(), 7)

	require.ErrorContains(t, err, "could not read the ready marks of pull request 7")
	assert.Nil(t, marks)
}
