package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
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
	return fields{"baseRefName": base, "baseRepository": fields{"nameWithOwner": in}, "closingIssuesReferences": closing, "body": "", "commits": commitList(0)}
}

// commitList is the commit page of the answer: the total and one node for each message.
func commitList(total int, messages ...string) fields {
	nodes := []fields{}
	for _, message := range messages {
		nodes = append(nodes, fields{"commit": fields{"message": message}})
	}

	return fields{"totalCount": total, "nodes": nodes}
}

// textOf is the answer for a pull request whose list holds nothing, as GitHub gives for a base that is not the default branch, with a body and commit messages.
func textOf(t *testing.T, base, body string, commits fields) string {
	t.Helper()

	pull := pullOf(base, repo, issueList(0))
	pull["body"], pull["commits"] = body, commits

	return refsOf(t, pull, fields{"name": "main"})
}

func answer(t *testing.T, base, in string, closing any) string {
	t.Helper()

	return refsOf(t, pullOf(base, in, closing), fields{"name": "main"})
}

func TestClosingRefs_OneCallGivesTheIssuesTheBaseAndTheDefaultBranch(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, answer(t, "main", repo, issueList(2, issue(751, repo), issue(12, repo))))

	refs, err := f.ClosingRefs(context.Background(), 792)
	require.NoError(t, err)
	assert.Equal(t, model.ClosingRefs{BaseBranch: "main", DefaultBranch: "main", Issues: []int{751, 12}, Elsewhere: []model.IssueRef{}}, refs)
	require.Len(t, run.Calls, 1, "one forge call for the whole check")
	assert.Equal(t, []string{"api", "graphql", "-f", "o=owner", "-f", "n=name", "-F", "p=792"}, run.Calls[0][:8])

	query := run.Calls[0][9]
	for _, field := range []string{"closingIssuesReferences(first:100)", "baseRefName", "baseRepository{nameWithOwner}", "defaultBranchRef{name}", "body", "commits(first:100)", "commit{message}", "totalCount", "repository{nameWithOwner}"} {
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

func TestClosingRefs_AnIssueOfAnotherRepositoryIsKeptAsAFullReferenceAndANodeWithNoRepositoryIsLeftOut(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, answer(t, "main", repo, issueList(5, issue(4, "Owner/Name"), issue(5, "Other-Org/Other-Repo"), fields{"number": 6}, issue(5, "other-org/other-repo"), issue(2, "third/repo"))))

	refs, err := f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, []int{4}, refs.Issues)
	assert.Equal(t, []model.IssueRef{"other-org/other-repo#5", "third/repo#2"}, refs.Elsewhere)
}

func TestClosingRefs_AnAnswerWhoseOnlyIssueIsInAnotherRepositoryKeepsThatIssueAndRefusesNothing(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, answer(t, "main", repo, issueList(1, issue(5, "other-org/other-repo"))))

	refs, err := f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.Empty(t, refs.Issues)
	assert.Equal(t, []model.IssueRef{"other-org/other-repo#5"}, refs.Elsewhere)
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
		{"no_issue_names_its_repository", answer(t, "main", repo, issueList(2, fields{"number": 5}, fields{"number": 6})), "pull request 7 lists 2 closing issues, and none names its repository; the answer cannot be trusted"},
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

func TestClosingRefs_APullRequestIntoAnotherBaseTakesTheIssuesOfTheBodyAndTheCommitMessages(t *testing.T) {
	t.Parallel()

	body := "Closes #12\nCloses #99"
	f, _ := fixed(t, textOf(t, "integration/r2", body, commitList(2, "add a\n\nfixes #5", "add b\n\nCloses #12")))

	refs, err := f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, model.ClosingRefs{BaseBranch: "integration/r2", DefaultBranch: "main", Issues: []int{5, 12, 99}, Elsewhere: []model.IssueRef{}}, refs)
}

func TestClosingRefs_AKeywordForAnotherRepositoryIsAFullReferenceAndOneForThisRepositoryIsANumber(t *testing.T) {
	t.Parallel()

	body := "Closes other/repo#3\nResolves Owner/Name#4\nCloses https://github.com/other/repo/issues/8\nFixes: https://github.com/OWNER/name/issues/9\nSee #50"
	f, _ := fixed(t, textOf(t, "integration/r2", body, commitList(0)))

	refs, err := f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, []int{4, 9}, refs.Issues)
	assert.Equal(t, []model.IssueRef{"other/repo#3", "other/repo#8"}, refs.Elsewhere)

	f, _ = fixed(t, textOf(t, "integration/r2", "Closes #99999999999999999999 and closes #6", commitList(0)))

	refs, err = f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, []int{6}, refs.Issues, "a number too large for an issue is no issue")
}

func TestClosingRefs_APullRequestIntoAnotherBaseWithNoKeywordHasAnEmptySetNotNil(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, textOf(t, "integration/r2", "Part of #5", commitList(1, "no keyword")))

	refs, err := f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.NotNil(t, refs.Issues)
	assert.Empty(t, refs.Issues)
}

func TestClosingRefs_APullRequestIntoTheDefaultBranchKeepsTheListOfGitHubAndReadsNoKeyword(t *testing.T) {
	t.Parallel()

	pull := pullOf("main", repo, issueList(1, issue(12, repo)))
	pull["body"], pull["commits"] = "Closes #77", commitList(1, "fixes #78")
	f, _ := fixed(t, refsOf(t, pull, fields{"name": "main"}))

	refs, err := f.ClosingRefs(context.Background(), 7)
	require.NoError(t, err)
	assert.Equal(t, []int{12}, refs.Issues)
}

func TestClosingRefs_CommitsPastOnePageOrNoCommitListRefuseForAnotherBase(t *testing.T) {
	t.Parallel()

	f, _ := fixed(t, textOf(t, "integration/r2", "", commitList(150, "fixes #5")))

	_, err := f.ClosingRefs(context.Background(), 7)
	require.ErrorIs(t, err, model.ErrMorePages)
	require.ErrorContains(t, err, "pull request 7 has 150 commits, more than one page")

	pull := pullOf("integration/r2", repo, issueList(0))
	pull["commits"] = nil
	f, _ = fixed(t, refsOf(t, pull, fields{"name": "main"}))

	_, err = f.ClosingRefs(context.Background(), 7)
	require.ErrorContains(t, err, "the answer holds no commit list")
}
