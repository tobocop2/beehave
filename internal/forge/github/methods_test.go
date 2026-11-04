package github_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

// The command lines come from gh help: `gh api --paginate --slurp`, `gh search prs --help`, `gh repo view --json`, `gh pr comment --body` and `gh pr ready --undo`.

func TestComments_ReadsEveryPageOfTheConversation(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, toJSON(t, []any{
		[]fields{{"user": fields{"login": "a"}, "created_at": "2026-01-01T00:00:00Z", "body": "one"}},
		[]fields{{"user": fields{"login": "b"}, "created_at": "2026-01-02T00:00:00Z", "body": "two"}},
	}))

	rows, err := f.Comments(context.Background(), 9)
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, []string{rows[0].Body, rows[1].Body})
	assert.Equal(t, "b", rows[1].User.Login)
	assert.Equal(t, [][]string{{"api", "--paginate", "--slurp", "repos/owner/name/issues/9/comments"}}, run.Calls)
}

func TestReviewComments_ReadsTheInlineComments(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, toJSON(t, []any{[]fields{{"user": fields{"login": "a"}, "path": "x.go", "line": 4, "body": "nit"}}}))

	rows, err := f.ReviewComments(context.Background(), 9)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "x.go", rows[0].Path)
	assert.Equal(t, 4, *rows[0].Line)
	assert.Equal(t, [][]string{{"api", "--paginate", "--slurp", "repos/owner/name/pulls/9/comments"}}, run.Calls)
}

func TestComments_RefuseAFailedOrMalformedRead(t *testing.T) {
	t.Parallel()

	f, _ := failing(t, "HTTP 502")
	_, err := f.Comments(context.Background(), 9)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not read the comments of #9")

	_, err = f.ReviewComments(context.Background(), 9)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not read the review comments of pull request 9")

	bad, _ := fixed(t, "not json")
	_, err = bad.Comments(context.Background(), 9)
	require.Error(t, err)
}

func TestSearch_AsksForOpenItemsThatInvolveTheUser(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `[{"number":3},{"number":8}]`)

	numbers, err := f.Search(context.Background(), model.SearchRequest{Kind: model.SearchPulls, Involves: "me", Since: "2026-01-01", Limit: 200})
	require.NoError(t, err)
	assert.Equal(t, []int{3, 8}, numbers)
	assert.Equal(t, [][]string{{
		"search", "prs", "--repo", repo, "--involves", "me", "--state", "open",
		"--updated", ">=2026-01-01", "--limit", "200", "--json", "number",
	}}, run.Calls)
}

func TestSearch_TakesOneRowUnderTheLimitAndRefusesARowThatFillsIt(t *testing.T) {
	t.Parallel()

	req := model.SearchRequest{Kind: model.SearchIssues, Involves: "me", Since: "2026-01-01", Limit: 2}

	under, _ := fixed(t, `[{"number":3}]`)
	numbers, err := under.Search(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, []int{3}, numbers)

	full, _ := fixed(t, `[{"number":3},{"number":4}]`)
	_, err = full.Search(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the issues search returned 2 of a limit of 2, so it may be cut off")
}

func TestSearch_RefusesAFailedOrMalformedAnswer(t *testing.T) {
	t.Parallel()

	req := model.SearchRequest{Kind: model.SearchIssues, Limit: 5}

	failed, _ := failing(t, "HTTP 502")
	_, err := failed.Search(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot search issues")

	bad, _ := fixed(t, `{"a":1}`)
	_, err = bad.Search(context.Background(), req)
	require.Error(t, err)
}

func TestDefaultBranch_ReadsTheNameOfTheDefaultBranchRef(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `{"defaultBranchRef":{"name":"trunk"}}`)

	name, err := f.DefaultBranch(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "trunk", name)
	assert.Equal(t, [][]string{{"repo", "view", repo, "--json", "defaultBranchRef"}}, run.Calls)
}

func TestDefaultBranch_RefusesAFailedReadAndAnEmptyName(t *testing.T) {
	t.Parallel()

	failed, _ := failing(t, "HTTP 502")
	_, err := failed.DefaultBranch(context.Background())
	require.Error(t, err)

	for _, stdout := range []string{`{"defaultBranchRef":{"name":""}}`, `{"defaultBranchRef":null}`, `{}`, "not json"} {
		f, _ := fixed(t, stdout)
		_, err := f.DefaultBranch(context.Background())
		require.Error(t, err, stdout)
	}
}

func TestBranchRef_ReturnsTheTipOfAnExistingBranch(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `{"object":{"sha":"abc"}}`)

	sha, found, err := f.BranchRef(context.Background(), "feat/x")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "abc", sha)
	assert.Equal(t, [][]string{{"api", "repos/owner/name/git/ref/heads/feat/x"}}, run.Calls)
}

func TestBranchRef_EscapesEachSegmentOfTheName(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, `{"object":{"sha":"abc"}}`)

	_, _, err := f.BranchRef(context.Background(), "feat/a#1?x y")
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"api", "repos/owner/name/git/ref/heads/feat/a%231%3Fx%20y"}}, run.Calls)
}

// realNotFound is what gh prints for a missing ref and for a missing repository alike.
const realNotFound = `{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}gh: Not Found (HTTP 404)`

func TestBranchRef_ANotFoundAnswerIsGoneOnlyWhenTheRepositoryReads(t *testing.T) {
	t.Parallel()

	gone := &proctest.Scripted{}
	gone.PlanFail(realNotFound, "api", "repos/owner/name/git/ref/heads/x")
	gone.Plan("owner/name\n", "api", "repos/owner/name", "--jq", ".full_name")

	sha, found, err := newForge(t, repo, gone).BranchRef(context.Background(), "x")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, sha)
	assert.True(t, gone.Called("api", "repos/owner/name", "--jq", ".full_name"))

	noRepo := &proctest.Scripted{}
	noRepo.PlanFail(realNotFound, "api", "repos/owner/name/git/ref/heads/x")
	noRepo.PlanFail(realNotFound, "api", "repos/owner/name", "--jq", ".full_name")

	_, found, err = newForge(t, repo, noRepo).BranchRef(context.Background(), "x")
	require.Error(t, err)
	assert.False(t, found)

	other := &proctest.Scripted{}
	other.PlanFail("HTTP 502", "api", "repos/owner/name/git/ref/heads/x")
	_, _, err = newForge(t, repo, other).BranchRef(context.Background(), "x")
	require.Error(t, err)
	assert.Len(t, other.Calls, 1)

	down, _ := failing(t, "HTTP 502")
	_, _, err = down.BranchRef(context.Background(), "x")
	require.Error(t, err)

	bad, _ := fixed(t, "not json")
	_, _, err = bad.BranchRef(context.Background(), "x")
	require.Error(t, err)

	empty, _ := fixed(t, `{"object":{"sha":""}}`)
	_, _, err = empty.BranchRef(context.Background(), "x")
	require.Error(t, err)
}

func TestPostComment_SendsTheBodyAsOneArgument(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.PostComment(context.Background(), 4, "line one\n--body x"))
	assert.Equal(t, [][]string{{"pr", "comment", "4", "--repo", repo, "--body", "line one\n--body x"}}, run.Calls)

	down, _ := failing(t, "HTTP 502")
	err := down.PostComment(context.Background(), 4, "hi")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot comment on pull request 4")
}

func TestSetDraft_MarksReadyOrConvertsToDraft(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.SetDraft(context.Background(), 4, false))
	require.NoError(t, f.SetDraft(context.Background(), 4, true))
	assert.Equal(t, [][]string{
		{"pr", "ready", "4", "--repo", repo},
		{"pr", "ready", "4", "--repo", repo, "--undo"},
	}, run.Calls)

	down, _ := failing(t, "HTTP 502")
	err := down.SetDraft(context.Background(), 4, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot convert pull request 4 to draft")
	err = down.SetDraft(context.Background(), 4, false)
	assert.Contains(t, err.Error(), "cannot mark pull request 4 ready")
}
