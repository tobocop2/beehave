package github_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// queryOf is the GraphQL query of a call of gh api graphql.
func queryOf(t *testing.T, call []string) string {
	t.Helper()

	for _, arg := range call {
		if query, found := strings.CutPrefix(arg, "query="); found {
			return query
		}
	}

	require.Fail(t, "the call holds no query", "%v", call)

	return ""
}

func TestOpenPulls_ReadsEveryPageWithTheAuthorAssociationInOneCall(t *testing.T) {
	t.Parallel()

	row := func(number int, association string) fields {
		return fields{"number": number, "headRefName": "b", "baseRefName": "main", "authorAssociation": association, "labels": labelNodes("P1", "hold")}
	}

	f, run := fixed(t, listPages(t, []fields{row(1, "OWNER"), row(2, "CONTRIBUTOR")}, []fields{{"number": 3, "headRefName": "b", "baseRefName": "main"}}))

	pulls, err := f.OpenPulls(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, pulls, 3, "the rows of every page join")
	require.Len(t, run.Calls, 1, "one call reads the whole list")

	assert.Equal(t, []model.Association{model.AssociationOwner, model.AssociationContributor, ""}, []model.Association{pulls[0].Association, pulls[1].Association, pulls[2].Association},
		"each row keeps what the forge says, and a row with none has none, which counts as foreign")
	assert.Equal(t, []string{"P1", "hold"}, pulls[0].Labels, "the label nodes become the label names")

	call := run.Calls[0]
	assert.Equal(t, []string{"api", "graphql", "--paginate", "--slurp", "-f", "o=owner", "-f", "n=name"}, call[:8])
	assert.NotContains(t, strings.Join(call, " "), "b=", "no base asks for every open pull request")

	query := queryOf(t, call)
	for _, part := range []string{"list:pullRequests(states:OPEN,baseRefName:$b,", "authorAssociation", "after:$endCursor", "pageInfo{hasNextPage endCursor}", "reviewDecision", "isDraft", "author{login}"} {
		assert.Contains(t, query, part)
	}
}

func TestOpenPulls_ABaseIsAVariableOfTheQuery(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, listPages(t, []fields{}))

	pulls, err := f.OpenPulls(context.Background(), "main")
	require.NoError(t, err)
	assert.Empty(t, pulls)
	assert.Equal(t, []string{"-f", "b=main"}, run.Calls[0][8:10])
}

func TestHistoryReads_ListTheBranchAndTheSearchInEveryState(t *testing.T) {
	t.Parallel()

	answer := listPages(t, []fields{{"number": 4, "title": "t", "body": "b", "state": "MERGED", "headRefName": "41", "baseRefName": "main", "mergedAt": "2026-11-01T00:00:00Z", "closedAt": "2026-11-01T00:00:00Z", "authorAssociation": "MEMBER"}})
	f, run := fixed(t, answer)

	rows, err := f.BranchPulls(context.Background(), "41")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, model.AssociationMember, rows[0].AuthorAssociation)
	assert.Equal(t, "MERGED", model.Deref(rows[0].State))
	assert.Equal(t, "41", model.Deref(rows[0].HeadRefName))
	assert.Equal(t, "2026-11-01T00:00:00Z", model.Deref(rows[0].ClosedAt))
	assert.Equal(t, []string{"-f", "o=owner", "-f", "n=name", "-f", "h=41"}, run.Calls[0][4:10], "the branch is a string variable")

	branchQuery := queryOf(t, run.Calls[0])
	assert.Contains(t, branchQuery, "list:pullRequests(headRefName:$h,")
	assert.NotContains(t, branchQuery, "states:", "no state filter lists every state")

	top, run := fixed(t, `[{"data":{"list":{"totalCount":1,"nodes":[{"number":6,"title":"names #41","authorAssociation":"OWNER"}]}}}]`)

	found, err := top.SearchPulls(context.Background(), "#41 in:title,body")
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, 6, found[0].Number)
	assert.Equal(t, model.AssociationOwner, found[0].AuthorAssociation)
	assert.Equal(t, []string{"-f", "q=repo:owner/name is:pr #41 in:title,body"}, run.Calls[0][4:6], "the search names the repository and pull requests only, and no other variable")

	searchQuery := queryOf(t, run.Calls[0])
	for _, part := range []string{"list:search(query:$q,type:ISSUE,", "totalCount:issueCount", "... on PullRequest{", "authorAssociation", "closedAt"} {
		assert.Contains(t, searchQuery, part)
	}
}

func TestListReads_RefuseAFailedCutOrUnreadableList(t *testing.T) {
	t.Parallel()

	down, _ := failing(t, "HTTP 502")
	_, err := down.OpenPulls(context.Background(), "")
	require.EqualError(t, err, "cannot list the open pull requests")

	cut, _ := fixed(t, `[{"data":{"repository":{"list":{"totalCount":3,"nodes":[{"number":1}]}}}}]`)
	_, err = cut.BranchPulls(context.Background(), "41")
	require.ErrorIs(t, err, model.ErrMorePages)
	require.ErrorContains(t, err, "cannot list the pull requests of branch 41: cannot read the answer of `gh api graphql` (1 of 3 rows, so the rest are unknown")

	odd, _ := fixed(t, `{"data":{}}`)
	_, err = odd.SearchPulls(context.Background(), "#41")
	require.ErrorContains(t, err, "cannot search the pull requests (#41)")
}
