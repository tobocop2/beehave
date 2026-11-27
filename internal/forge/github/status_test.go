package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/forge"
	"github.com/tobocop2/beehest/internal/forge/forgetest"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc/proctest"
)

func TestStatuses_ConformsToTheStatusesRules(t *testing.T) {
	t.Parallel()

	rules := toJSON(t, []any{[]fields{
		{"type": "deletion"},
		{"type": "pull_request", "parameters": fields{"required_approving_review_count": 0}},
		{"type": "required_status_checks", "parameters": fields{"required_status_checks": []fields{{"context": forgetest.StatusGate}, {"context": forgetest.StatusReview}}}},
		{"type": "required_status_checks", "parameters": fields{"required_status_checks": []fields{{"context": forgetest.StatusGate}}}},
	}})

	run := &proctest.Scripted{}
	run.Plan("", "api", "-X", "POST", "repos/owner/name/statuses/"+forgetest.StatusCommit)
	run.Plan(rules, "api", "--paginate", "--slurp", "repos/owner/name/rules/branches/"+forgetest.StatusBranch)
	forgetest.CheckStatuses(t, newForge(t, repo, run))

	down, _ := failing(t, "boom")
	forgetest.CheckStatusesFails(t, down)
}

func TestPostStatus_PostsTheContextTheStateAndTheDescriptionToTheCommit(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	err := f.PostStatus(context.Background(), model.CommitStatus{SHA: tip, Context: "beehest/gate", State: model.StatusSuccess, Description: "passed"})
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"api", "-X", "POST", "repos/" + repo + "/statuses/" + tip, "-f", "state=success", "-f", "context=beehest/gate", "-f", "description=passed"}}, run.Calls)
}

func TestPostStatus_AnEmptyDescriptionSendsNoDescriptionField(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.PostStatus(context.Background(), model.CommitStatus{SHA: tip, Context: "beehest/review", State: model.StatusPending}))
	assert.Equal(t, [][]string{{"api", "-X", "POST", "repos/" + repo + "/statuses/" + tip, "-f", "state=pending", "-f", "context=beehest/review"}}, run.Calls)
}

func TestPostStatus_RefusesAStatusThatStandsForNoCommitBeforeGhRuns(t *testing.T) {
	t.Parallel()

	good := model.CommitStatus{SHA: tip, Context: "beehest/gate", State: model.StatusSuccess}
	short, noContext, badState, long := good, good, good, good
	short.SHA = tip[:7]
	noContext.Context = ""
	badState.State = "done"
	long.Description = strings.Repeat("x", 141)

	cases := map[string]model.CommitStatus{"a short commit id": short, "no context": noContext, "an unknown state": badState, "a long description": long}

	for name, status := range cases {
		f, run := fixed(t, "")

		require.Error(t, f.PostStatus(context.Background(), status), name)
		assert.Empty(t, run.Calls, name)
	}
}

func TestPostStatus_AFailedPostIsARefusalThatKeepsTheMessageOfTheForge(t *testing.T) {
	t.Parallel()

	f, _ := failing(t, "gh: Resource not accessible (HTTP 403)")

	err := f.PostStatus(context.Background(), model.CommitStatus{SHA: tip, Context: "beehest/gate", State: model.StatusSuccess})
	require.ErrorAs(t, err, new(*forge.Error))
	require.ErrorContains(t, err, "cannot post the status beehest/gate on commit "+tip)
	assert.ErrorContains(t, err, "Resource not accessible (HTTP 403)")
}

func TestProtection_ABranchWithNoRuleHasAnEmptyProtectionAndNoError(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "[[]]")

	got, err := f.Protection(context.Background(), "integration/r2")
	require.NoError(t, err)
	assert.Equal(t, model.Protection{PullRequest: false, RequiredStatuses: []string{}}, got)
	assert.Equal(t, [][]string{{"api", "--paginate", "--slurp", "repos/" + repo + "/rules/branches/integration/r2"}}, run.Calls)
}

func TestProtection_ARuleOtherThanTheTwoIsNotAPullRequestRule(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, `[[{"type":"deletion"},{"type":"non_fast_forward"}]]`)

	got, err := f.Protection(context.Background(), "main")
	require.NoError(t, err)
	assert.False(t, got.PullRequest)
	assert.Empty(t, got.RequiredStatuses)
}

func TestProtection_AnAnswerThatIsNotAListOfRulesIsARefusalNeverAnEmptyProtection(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, `{"message":"nope"}`)

	_, err := f.Protection(context.Background(), "main")
	require.ErrorContains(t, err, "cannot read the rules of the branch main")
}
