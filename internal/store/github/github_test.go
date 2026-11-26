package github_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
	"github.com/tobocop2/beehave/internal/store"
	"github.com/tobocop2/beehave/internal/store/github"
)

const ghRepo = "owner/name"

func newGithub(t *testing.T) (*github.Store, *proctest.Scripted) {
	t.Helper()

	run := &proctest.Scripted{}
	gh, err := github.New(ghRepo, run)
	require.NoError(t, err)

	return gh, run
}

func codeOf(t *testing.T, err error) int {
	t.Helper()

	var failure *store.Error
	require.ErrorAs(t, err, &failure)

	return failure.Code
}

// issue is one open issue as the GraphQL list gives it.
func issue(number int, title string, labels ...string) map[string]any {
	names := make([]map[string]string, len(labels))
	for i, label := range labels {
		names[i] = map[string]string{"name": label}
	}

	return map[string]any{"number": number, "title": title, "labels": map[string]any{"nodes": names}, "assignees": map[string]any{"nodes": []any{}}, "authorAssociation": "OWNER"}
}

// issuePages is the answer of gh api graphql --paginate --slurp: one page for each slice of rows, each with the total of all rows.
func issuePages(t *testing.T, pages ...[]map[string]any) string {
	t.Helper()

	total := 0
	for _, page := range pages {
		total += len(page)
	}

	answer := make([]any, len(pages))
	for i, page := range pages {
		answer[i] = map[string]any{"data": map[string]any{"repository": map[string]any{"list": map[string]any{"totalCount": total, "nodes": page}}}}
	}

	return toJSON(t, answer)
}

func TestNew_NeedsOwnerAndName(t *testing.T) {
	t.Parallel()

	for _, repo := range []string{"", "name", "/name", "owner/", "a/b/c", `o/"x`, "o/n m", `o/n\m`, "o/n;m", "o/é", "o w/n"} {
		_, err := github.New(repo, &proctest.Scripted{})
		require.Error(t, err, repo)
		assert.Equal(t, store.CodeRefused, codeOf(t, err))
		assert.Contains(t, err.Error(), "the repository must be written owner/name")
	}
}

func TestNew_TakesLettersDigitsDotsUnderscoresAndHyphens(t *testing.T) {
	t.Parallel()

	for _, repo := range []string{"tobocop2/beehave", "A.b_c-1/Z.y_x-0"} {
		_, err := github.New(repo, &proctest.Scripted{})
		require.NoError(t, err, repo)
	}
}

func TestGithubItemList_SkipsEpicsAndReadsLabels(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	crowded := issue(3, "has two", "P2", "P1", "hold")
	crowded["assignees"] = map[string]any{"nodes": []map[string]string{{"login": "ann"}, {"login": "bob"}}}
	run.Plan(issuePages(t, []map[string]any{issue(1, "plain"), issue(2, "epic: big", "P0"), crowded}, []map[string]any{
		issue(4, "holding", "holder", "P3"), issue(5, "not an epic: x", "P1"), issue(6, "no hold", "Hold", "P4"),
	}), "api", "graphql")

	items, err := gh.ItemList(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, []model.Item{
		{ID: "1", Severity: model.SeverityUnknown, Title: "plain", Association: model.AssociationOwner},
		{ID: "3", Severity: model.SeverityP2, Hold: true, Assignee: "ann,bob", Title: "has two", Labels: []string{"P2", "P1", "hold"}, Association: model.AssociationOwner},
		{ID: "4", Severity: model.SeverityP3, Title: "holding", Labels: []string{"holder", "P3"}, Association: model.AssociationOwner},
		{ID: "5", Severity: model.SeverityP1, Title: "not an epic: x", Labels: []string{"P1"}, Association: model.AssociationOwner},
		{ID: "6", Severity: model.SeverityUnknown, Title: "no hold", Labels: []string{"Hold", "P4"}, Association: model.AssociationOwner},
	}, items, "the rows of every page join, in the order of the forge")
	require.Len(t, run.Calls, 1, "one call reads every page")
	assert.Equal(t, []string{"api", "graphql", "--paginate", "--slurp", "-f", "o=owner", "-f", "n=name"}, run.Calls[0][:8])
	assert.NotContains(t, strings.Join(run.Calls[0], " "), "l[]=", "no label filters nothing")

	query := strings.TrimPrefix(run.Calls[0][len(run.Calls[0])-1], "query=")
	for _, part := range []string{"list:issues(states:OPEN,labels:$l,", "authorAssociation", "after:$endCursor", "pageInfo{hasNextPage endCursor}", "assignees(first:100){nodes{login}}"} {
		assert.Contains(t, query, part)
	}
}

func TestItemList_ReadsTheAuthorAssociation(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	foreign := issue(2, "from a contributor")
	foreign["authorAssociation"] = "CONTRIBUTOR"
	missing := issue(3, "no association")
	delete(missing, "authorAssociation")
	run.Plan(issuePages(t, []map[string]any{issue(1, "own"), foreign, missing}), "api", "graphql")

	items, err := gh.ItemList(context.Background(), "")
	require.NoError(t, err)

	got := make([]model.Association, len(items))
	for i, item := range items {
		got[i] = item.Association
	}

	assert.Equal(t, []model.Association{model.AssociationOwner, model.AssociationContributor, ""}, got, "the list keeps what the forge says, and a row with none has none")
}

func TestGithubReads_RefuseWhatTheyCannotTrust(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stdout string
		fail   bool
		want   string
	}{
		{name: "failed_list", fail: true, want: "cannot list the open issues"},
		{name: "cut_list", stdout: `[{"data":{"repository":{"list":{"totalCount":3,"nodes":[{"number":1,"title":"t"}]}}}}]`, want: "1 of 3 rows, so the rest are unknown"},
		{name: "not_a_list_of_pages", stdout: `{"number":1}`, want: "cannot read the answer of `gh api graphql`"},
		{name: "page_with_no_list", stdout: `[{"data":{"repository":null}}]`, want: "a page holds no list"},
		{name: "null_answer", stdout: "null", want: "the answer is null"},
		{name: "text_after_the_answer", stdout: "[] []", want: "after top-level value"},
		{name: "row_that_is_not_an_object", stdout: `[{"data":{"repository":{"list":{"totalCount":1,"nodes":[3]}}}}]`, want: "cannot read the answer"},
		{name: "no_title", stdout: `[{"data":{"repository":{"list":{"totalCount":1,"nodes":[{"number":1}]}}}}]`, want: "no readable title"},
		{name: "no_number", stdout: `[{"data":{"repository":{"list":{"totalCount":1,"nodes":[{"title":"t"}]}}}}]`, want: "no readable number"},
		{name: "number_as_text", stdout: `[{"data":{"repository":{"list":{"totalCount":1,"nodes":[{"number":"1","title":"t"}]}}}}]`, want: "cannot read the answer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gh, run := newGithub(t)
			if tt.fail {
				run.PlanFail("boom", "api", "graphql")
			} else {
				run.Plan(tt.stdout, "api", "graphql")
			}

			_, err := gh.ItemList(context.Background(), "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, store.CodeRefused, codeOf(t, err))
		})
	}
}

func TestGithubItemList_WithALabelAsksGithubForThatLabel(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.Plan(issuePages(t, []map[string]any{issue(1, "tagged", "bug")}), "api", "graphql")

	items, err := gh.ItemList(context.Background(), "bug")
	require.NoError(t, err)
	assert.Len(t, items, 1)
	assert.True(t, run.Called("-f", "l[]=bug"), "the label is a list variable of the query")
}

// TestGithubItemList_TheQueryAsksForTheAuthorAssociationOfEveryIssue reads the query text. The fake answer carries the association whatever
// the query asks, so only the query shows that a real forge would send it. An issue without it counts as foreign.
func TestGithubItemList_TheQueryAsksForTheAuthorAssociationOfEveryIssue(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.Plan(issuePages(t, []map[string]any{}), "api", "graphql")

	_, err := gh.ItemList(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, run.Calls, 1)

	var query string

	for _, arg := range run.Calls[0] {
		if text, found := strings.CutPrefix(arg, "query="); found {
			query = text
		}
	}

	assert.Contains(t, query, "list:issues(")
	assert.Contains(t, query, "authorAssociation", "the rows of the list carry the association")
}

func TestGithubItemList_ListsMoreThanAThousandIssues(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.Plan(issuePages(t, fill(1000, nil), fill(1, nil)), "api", "graphql")

	items, err := gh.ItemList(context.Background(), "")
	require.NoError(t, err)
	assert.Len(t, items, 1001, "every page is read, so no limit cuts the list")
}

func TestGithubItemList_NullLabelsAndAssigneesAreEmpty(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.Plan(`[{"data":{"repository":{"list":{"totalCount":2,"nodes":[{"number":7,"title":"t","labels":null,"assignees":null},{"number":8,"title":"u","labels":{"nodes":null},"assignees":{"nodes":null}}]}}}}]`, "api", "graphql")

	items, err := gh.ItemList(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, []model.Item{{ID: "7", Severity: model.SeverityUnknown, Title: "t"}, {ID: "8", Severity: model.SeverityUnknown, Title: "u"}}, items)
}

func TestDecodeRefusals_KeepTheirCause(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.Plan("null", "api", "graphql")

	_, err := gh.ItemList(context.Background(), "")
	require.ErrorIs(t, err, model.ErrNullAnswer)

	gh, run = newGithub(t)
	run.Plan(`[{"data":{"repository":{"list":{"totalCount":2,"nodes":[]}}}}]`, "api", "graphql")

	_, err = gh.ItemList(context.Background(), "")
	require.ErrorIs(t, err, model.ErrMorePages)

	for _, answer := range []string{"nope", "[] []"} {
		gh, run = newGithub(t)
		run.Plan(answer, "api", "graphql")

		_, err = gh.ItemList(context.Background(), "")

		var syntax *json.SyntaxError
		require.ErrorAs(t, err, &syntax, answer)
		require.ErrorContains(t, err, "gh api graphql", answer)
	}
}
