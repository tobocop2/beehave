package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

// refsOf builds the answer of the closing-reference query. The pull request holds the base, its repository and the issue list; the repository holds the default branch.
func refsOf(t *testing.T, pull fields, defaultBranch any) string {
	t.Helper()

	return toJSON(t, fields{"data": fields{"repository": fields{
		"defaultBranchRef": defaultBranch,
		"pullRequest":      pull,
	}}})
}

func issueList(total int, nodes ...fields) fields {
	return fields{"totalCount": total, "nodes": append([]fields{}, nodes...)}
}

func issue(number int, in string) fields {
	return fields{"number": number, "repository": fields{"nameWithOwner": in}}
}

func pullOf(base, in string, closing any) fields {
	return fields{"baseRefName": base, "baseRepository": fields{"nameWithOwner": in}, "closingIssuesReferences": closing}
}

func answer(t *testing.T, base, in string, closing any) string {
	t.Helper()

	return refsOf(t, pullOf(base, in, closing), fields{"name": "main"})
}

func TestClosingRefs_OneCallGivesTheIssuesTheBaseAndTheDefaultBranch(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, answer(t, "integration/r7", repo, issueList(2, issue(751, repo), issue(12, repo))))

	refs, err := f.ClosingRefs(context.Background(), 792)
	require.NoError(t, err)
	assert.Equal(t, model.ClosingRefs{BaseBranch: "integration/r7", DefaultBranch: "main", Issues: []int{751, 12}}, refs)
	require.Len(t, run.Calls, 1, "one forge call for the whole check")
	assert.Equal(t, []string{"api", "graphql", "-f", "o=owner", "-f", "n=name", "-F", "p=792"}, run.Calls[0][:8])

	query := run.Calls[0][9]
	for _, field := range []string{"closingIssuesReferences(first:100)", "baseRefName", "baseRepository{nameWithOwner}", "defaultBranchRef{name}", "totalCount", "repository{nameWithOwner}"} {
		assert.Contains(t, query, field)
	}
}

func TestClosingRefs_AnIssueIsKeptWhenItsRepositoryIsTheBaseRepositoryOfTheAnswer(t *testing.T) {
	t.Parallel()

	// The origin remote says owner/name. GitHub says the repository is now new/home. The answer decides.
	f, _ := fixed(t, answer(t, "main", "new/home", issueList(1, issue(4, "New/Home"))))

	refs, err := f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, []int{4}, refs.Issues)
}

func TestClosingRefs_AnIssueOfAnotherRepositoryIsLeftOut(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, answer(t, "main", repo, issueList(3, issue(4, "Owner/Name"), issue(5, "other/repo"), fields{"number": 6})))

	refs, err := f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, []int{4}, refs.Issues)
}

func TestClosingRefs_NoIssueIsAnEmptyListNotAnError(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, answer(t, "main", repo, issueList(0)))

	refs, err := f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.NotNil(t, refs.Issues)
	assert.Empty(t, refs.Issues)
}

func TestClosingRefs_ANullBaseOrNullDefaultBranchComesBackEmptyForTheCallerToRefuse(t *testing.T) {
	t.Parallel()

	pull := fields{"baseRefName": nil, "baseRepository": fields{"nameWithOwner": repo}, "closingIssuesReferences": issueList(0)}
	f, _ := fixed(t, refsOf(t, pull, nil))

	refs, err := f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.Empty(t, refs.BaseBranch)
	assert.Empty(t, refs.DefaultBranch)
}

func TestClosingRefs_ARefusalNamesThePullRequestAndNeverGuesses(t *testing.T) {
	t.Parallel()

	const base = "cannot read the issues that pull request 7 closes"

	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{"more_issues_than_one_page", answer(t, "main", repo, issueList(101)), "pull request 7 closes 101 issues, more than one page; the rest are unknown"},
		{"every_issue_dropped", answer(t, "main", repo, issueList(2, issue(5, "other/repo"), issue(6, "third/repo"))), "pull request 7 lists 2 closing issues, and none is in " + repo + "; the answer cannot be trusted"},
		{"an_empty_object", "{}", base + ": the answer holds none"},
		{"no_pull_request", `{"data":{"repository":null}}`, base + ": the answer holds none"},
		{"no_issue_list", answer(t, "main", repo, nil), base + ": the answer holds none"},
		{"no_node_list", answer(t, "main", repo, fields{"totalCount": 0}), base + ": the answer holds no issue list"},
		{"no_base_repository", refsOf(t, fields{"baseRefName": "main", "closingIssuesReferences": issueList(0)}, fields{"name": "main"}), base + ": the answer holds no base repository"},
		{"not_json", "not json", base + ": cannot read the answer of `gh api graphql`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, _ := fixed(t, tt.stdout)

			_, err := f.ClosingRefs(context.Background(), 7)
			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), tt.want), err.Error())
		})
	}
}

func TestClosingRefs_MoreThanOnePageWrapsTheMorePagesMarker(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, answer(t, "main", repo, issueList(150, issue(1, repo))))

	_, err := f.ClosingRefs(context.Background(), 7)
	require.ErrorIs(t, err, model.ErrMorePages)
}

func TestClosingRefs_AFailedReadIsRefused(t *testing.T) {
	t.Parallel()

	f, _ := failing(t, "boom")

	_, err := f.ClosingRefs(context.Background(), 7)
	require.EqualError(t, err, "cannot read the issues that pull request 7 closes")
}
