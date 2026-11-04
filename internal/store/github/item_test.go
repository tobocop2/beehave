package github_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/proc/proctest"
)

func TestGithubComment_ANullPageOrItemIsAnAnswerOfTheWrongShape(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{"null_page": "[[],null]", "null_item": "[[null]]"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gh, run := newGithub(t)
			run.Plan(body, "api", "--paginate")

			err := gh.Comment(context.Background(), "5", "k", "text")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "is null")
			assert.Len(t, run.Calls, 1)
		})
	}
}

func TestGithubItem_RefusesAnIdThatIsNotANumberWithoutCallingGh(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)

	_, err := gh.Item(context.Background(), "abc")
	require.Error(t, err)
	assert.Empty(t, run.Calls)
}

func TestGithubItemFields_DeclareIDTitleAndBody(t *testing.T) {
	t.Parallel()

	gh, _ := newGithub(t)

	assert.Equal(t, []string{"id", "title", "body", "severity"}, gh.ItemFields())
}

func TestGithubItem_ReadsTheDeclaredFields(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.Plan(`{"number":5,"title":"Parser","body":"It drops rows.","labels":[{"name":"bug"},{"name":"P1"}]}`, "issue", "view")

	got, err := gh.Item(context.Background(), "5")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"id": "5", "title": "Parser", "body": "It drops rows.", "severity": "P1"}, got)
	assert.Equal(t, []string{"issue", "view", "5", "--repo", ghRepo, "--json", "number,title,body,labels"}, run.Calls[0])
}

func TestGithubItem_ANullBodyIsEmpty(t *testing.T) {
	t.Parallel()

	gh, run := newGithub(t)
	run.Plan(`{"number":5,"title":"Parser","body":null}`, "issue", "view")

	got, err := gh.Item(context.Background(), "5")
	require.NoError(t, err)
	assert.Empty(t, got["body"])
}

func TestGithubItem_Refuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*proctest.Scripted)
		want  string
	}{
		{name: "view_fails", setup: func(r *proctest.Scripted) { r.PlanFail("x", "issue", "view") }, want: "cannot read item 5"},
		{name: "empty_answer", setup: func(r *proctest.Scripted) { r.Plan("", "issue", "view") }, want: "cannot read the answer"},
		{name: "no_title", setup: func(r *proctest.Scripted) { r.Plan(`{"number":5}`, "issue", "view") }, want: "no readable title"},
		{name: "no_number", setup: func(r *proctest.Scripted) { r.Plan(`{"title":"T"}`, "issue", "view") }, want: "no readable number"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gh, run := newGithub(t)
			tt.setup(run)

			_, err := gh.Item(context.Background(), "5")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}
