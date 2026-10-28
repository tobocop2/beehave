package github_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
	"github.com/tobocop2/obee/internal/store"
	"github.com/tobocop2/obee/internal/store/github"
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

func issue(number int, title string, labels ...string) map[string]any {
	names := make([]map[string]string, len(labels))
	for i, label := range labels {
		names[i] = map[string]string{"name": label}
	}

	return map[string]any{"number": number, "title": title, "labels": names, "assignees": []any{}}
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

	for _, repo := range []string{"tobocop2/obee", "A.b_c-1/Z.y_x-0"} {
		_, err := github.New(repo, &proctest.Scripted{})
		require.NoError(t, err, repo)
	}
}

func TestGithubOpenItems_SkipsEpicsAndReadsLabels(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	crowded := issue(3, "has two", "P2", "P1", "hold")
	crowded["assignees"] = []map[string]string{{"login": "ann"}, {"login": "bob"}}
	run.Plan(toJSON(t, []any{
		issue(1, "plain"), issue(2, "epic: big", "P0"), crowded,
		issue(4, "holding", "holder", "P3"), issue(5, "not an epic: x", "P1"), issue(6, "no hold", "Hold", "P4"),
	}), "issue", "list")

	items, err := gh.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []model.Item{
		{ID: "1", Severity: model.SeverityUnknown, Title: "plain"},
		{ID: "3", Severity: model.SeverityP2, Hold: true, Assignee: "ann,bob", Title: "has two"},
		{ID: "4", Severity: model.SeverityP3, Title: "holding"},
		{ID: "5", Severity: model.SeverityP1, Title: "not an epic: x"},
		{ID: "6", Severity: model.SeverityUnknown, Title: "no hold"},
	}, items)
	assert.Equal(t, []string{"issue", "list", "--repo", ghRepo, "--state", "open", "--limit", "1000", "--json", "number,title,labels,assignees"}, run.Calls[0])
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
		{name: "full_page", stdout: toJSON(t, fill(1000, nil)), want: "1000 rows, the list may be cut off"},
		{name: "not_a_list", stdout: `{"number":1}`, want: "cannot read the answer of `gh issue list`"},
		{name: "null_answer", stdout: "null", want: "the answer is null"},
		{name: "text_after_the_answer", stdout: "[] []", want: "after top-level value"},
		{name: "element_that_is_not_an_object", stdout: `[3]`, want: "cannot read the answer"},
		{name: "no_title", stdout: `[{"number":1}]`, want: "no readable title"},
		{name: "no_number", stdout: `[{"title":"t"}]`, want: "no readable number"},
		{name: "number_as_text", stdout: `[{"number":"1","title":"t"}]`, want: "cannot read the answer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gh, run := newGithub(t)
			if tt.fail {
				run.PlanFail("boom", "issue", "list")
			} else {
				run.Plan(tt.stdout, "issue", "list")
			}

			_, err := gh.OpenItems(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
			assert.Equal(t, store.CodeRefused, codeOf(t, err))
		})
	}
}

func TestGithubOpenItems_ListsAPageOneUnderFull(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.Plan(toJSON(t, fill(999, nil)), "issue", "list")

	items, err := gh.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Len(t, items, 999)
}

func TestGithubOpenItems_NullLabelsAndAssigneesAreEmpty(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.Plan(`[{"number":7,"title":"t","labels":null,"assignees":null}]`, "issue", "list")

	items, err := gh.OpenItems(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []model.Item{{ID: "7", Severity: model.SeverityUnknown, Title: "t"}}, items)
}

func TestDecodeRefusals_KeepTheirCause(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.Plan("null", "issue", "list")

	_, err := gh.OpenItems(context.Background())
	require.ErrorIs(t, err, model.ErrNullAnswer)

	for _, answer := range []string{"nope", "[] []"} {
		gh, run = newGithub(t)
		run.Plan(answer, "issue", "list")

		_, err = gh.OpenItems(context.Background())

		var syntax *json.SyntaxError
		require.ErrorAs(t, err, &syntax, answer)
		require.ErrorContains(t, err, "gh issue list", answer)
	}
}
