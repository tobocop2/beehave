package github_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/github"
)

var _ store.Reader = (*github.Store)(nil)

func readerOver(t *testing.T, run *proctest.Scripted) *github.Store {
	t.Helper()

	gh, err := github.New(ghRepo, run)
	require.NoError(t, err)

	return gh
}

func TestComments_ReadsEveryPageAndDropsTheMarkerLine(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(`[[{"body":"first"}],[{"body":"second\n\nobee-key: run-1:note"}]]`, "api", "--paginate", "--slurp", "repos/"+ghRepo+"/issues/3/comments")

	got, err := readerOver(t, run).Comments(t.Context(), "3")
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "second"}, got)
}

func TestComments_RefusesAFailedReadAndAnIdThatIsNotANumber(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.PlanFail("boom", "api")

	_, err := readerOver(t, run).Comments(t.Context(), "3")
	require.Error(t, err)

	_, err = readerOver(t, run).Comments(t.Context(), "x")
	require.Error(t, err)
}

func TestParent_ReadsTheParentIssueEndpoint(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(`{"number":9,"title":"epic"}`, "api", "repos/"+ghRepo+"/issues/3/parent")

	got, err := readerOver(t, run).Parent(t.Context(), "3")
	require.NoError(t, err)
	assert.Equal(t, "9", got)
}

func TestParent_A404ForAMissingIssueIsARefusalAndNotNoParent(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.PlanResult(model.CommandResult{ExitCode: 1, Stderr: "gh: Not Found (HTTP 404)"}, "api", "repos/"+ghRepo+"/issues/3/parent")
	run.PlanFail("GraphQL: Could not resolve to an issue or pull request with the number of 3. (HTTP 404)", "issue", "view", "3")

	got, err := readerOver(t, run).Parent(t.Context(), "3")
	require.Error(t, err, "an issue that cannot be read has no known parent")
	assert.Empty(t, got)
}

func TestParent_AnIssueWithNoParentIsNotAnErrorButAFailedReadIs(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.PlanResult(model.CommandResult{ExitCode: 1, Stderr: "gh: Not Found (HTTP 404)"}, "api", "repos/"+ghRepo+"/issues/3/parent")
	run.Plan(`{"number":3,"title":"a child"}`, "issue", "view", "3")

	got, err := readerOver(t, run).Parent(t.Context(), "3")
	require.NoError(t, err)
	assert.Empty(t, got)

	run = &proctest.Scripted{}
	run.PlanFail("gh: Server Error (HTTP 500)", "api")

	_, err = readerOver(t, run).Parent(t.Context(), "3")
	require.ErrorContains(t, err, "cannot read the parent of item 3")

	_, err = readerOver(t, run).Parent(t.Context(), "x")
	require.Error(t, err)

	run = &proctest.Scripted{}
	run.Plan("not json", "api")

	_, err = readerOver(t, run).Parent(t.Context(), "3")
	require.Error(t, err, "an answer that is not JSON is unreadable, not an issue with no parent")
}
