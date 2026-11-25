package github_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func TestCreatePR_OpensADraftAndReadsTheNumberFromTheAddress(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "https://github.com/"+repo+"/pull/42\n")
	req := model.NewPull{Head: "bb-1", Base: "round-1", Title: "--admin title", Body: "line one\nline two"}

	number, err := f.CreatePR(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, 42, number)
	assert.Equal(t, [][]string{{"pr", "create", "--draft", "--repo", repo, "--head", "bb-1", "--base", "round-1", "--title", "--admin title", "--body", "line one\nline two"}}, run.Calls)

	down, _ := failing(t, "HTTP 502")
	_, err = down.CreatePR(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot open a draft pull request for branch bb-1 into round-1")

	odd, _ := fixed(t, "created\n")
	_, err = odd.CreatePR(context.Background(), req)
	require.Error(t, err)

	huge, _ := fixed(t, "https://github.com/"+repo+"/pull/99999999999999999999\n")
	_, err = huge.CreatePR(context.Background(), req)
	require.Error(t, err)
}

func TestLinkStack_LinksTheChildOnTopOfTheParentByAddress(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")
	pulls := []string{"https://github.com/" + repo + "/pull/3", "https://github.com/" + repo + "/pull/4"}

	require.NoError(t, f.LinkStack(context.Background(), 3, 4, ""))
	assert.Equal(t, [][]string{append([]string{"stack", "link"}, pulls...)}, run.Calls, "no base means no --base flag")

	down, _ := failing(t, "HTTP 502")
	err := down.LinkStack(context.Background(), 3, 4, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot link pull request 4 into the stack of pull request 3")
}

func TestLinkStack_ABaseGoesToGhAsTheBaseOfTheBottom(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.LinkStack(context.Background(), 3, 4, "round-1"))
	assert.Equal(t, [][]string{{"stack", "link", "--base", "round-1", "https://github.com/" + repo + "/pull/3", "https://github.com/" + repo + "/pull/4"}}, run.Calls)
}

func TestUnstack_RemovesTheStackByItsNumber(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, graph(t, fields{"stack": fields{"number": 7}}))

	require.NoError(t, f.Unstack(context.Background(), 3))
	assert.Equal(t, []string{"stack", "unstack", "7"}, run.Calls[len(run.Calls)-1])
}

func TestUnstack_Refusals(t *testing.T) {
	t.Parallel()

	for name, pull := range map[string]fields{"no_stack": {"stack": nil}, "stack_number_zero": {"stack": fields{"number": 0}}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f, run := fixed(t, graph(t, pull))
			err := f.Unstack(context.Background(), 3)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "cannot unstack pull request 3: it is in no stack")
			assert.False(t, run.Called("stack", "unstack"))
		})
	}

	down, _ := failing(t, "HTTP 502")
	require.Error(t, down.Unstack(context.Background(), 3))

	f, run := fixed(t, graph(t, fields{"stack": fields{"number": 7}}))
	run.PlanFail("no", "stack", "unstack")
	err := f.Unstack(context.Background(), 3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot unstack pull request 3")
}

const prAddress = "https://github.com/" + repo + "/pull/42\n"

func TestCreatePR_ALabelGoesToTheSameCreateCallAndAnExistingLabelIsNotCreatedAgain(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(toJSON(t, []fields{{"name": "Made-By-Beehave"}}), "label", "list")
	run.Plan(prAddress, "pr", "create")
	f := newForge(t, repo, run)

	number, err := f.CreatePR(context.Background(), model.NewPull{Head: "bb-1", Base: "round-1", Title: "t", Body: "b", Label: "made-by-beehave"})
	require.NoError(t, err)
	assert.Equal(t, 42, number)
	assert.Equal(t, [][]string{
		{"label", "list", "--repo", repo, "--search", "made-by-beehave", "--json", "name", "--limit", "100"},
		{"pr", "create", "--draft", "--repo", repo, "--head", "bb-1", "--base", "round-1", "--title", "t", "--body", "b", "--label", "made-by-beehave"},
	}, run.Calls, "one create call carries the label, and no second write adds it")
}

func TestCreatePR_ARepositoryWithoutTheLabelGetsItBeforeThePullRequest(t *testing.T) {
	t.Parallel()

	run := &proctest.Scripted{}
	run.Plan(toJSON(t, []fields{{"name": "made-by-beehave-old"}}), "label", "list")
	run.Plan("", "label", "create")
	run.Plan(prAddress, "pr", "create")
	f := newForge(t, repo, run)

	_, err := f.CreatePR(context.Background(), model.NewPull{Head: "bb-1", Base: "round-1", Label: "made-by-beehave"})
	require.NoError(t, err)
	require.Len(t, run.Calls, 3)
	assert.Equal(t, []string{"label", "create", "made-by-beehave", "--repo", repo}, run.Calls[1])
	assert.Equal(t, "pr", run.Calls[2][0])
}

func TestCreatePR_AFailedLabelStepOpensNoPullRequest(t *testing.T) {
	t.Parallel()

	req := model.NewPull{Head: "bb-1", Base: "round-1", Label: "made-by-beehave"}

	listDown := &proctest.Scripted{}
	listDown.Plan(prAddress, "pr", "create")
	_, err := newForge(t, repo, listDown).CreatePR(context.Background(), req)
	require.Error(t, err, "a list that fails is not read as a repository with no label")
	assert.Contains(t, err.Error(), "cannot list the labels of "+repo)
	assert.Len(t, listDown.Calls, 1)

	odd := &proctest.Scripted{}
	odd.Plan("not json", "label", "list")
	_, err = newForge(t, repo, odd).CreatePR(context.Background(), req)
	require.Error(t, err)
	assert.Len(t, odd.Calls, 1)

	createDown := &proctest.Scripted{}
	createDown.Plan("[]", "label", "list")
	_, err = newForge(t, repo, createDown).CreatePR(context.Background(), req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot create the label "made-by-beehave"`)
	assert.Len(t, createDown.Calls, 2)
}

func TestClosePR_ClosesWithTheCommentAsOneArgument(t *testing.T) {
	t.Parallel()

	f, run := fixed(t, "")

	require.NoError(t, f.ClosePR(context.Background(), 690, "--admin replaced"))
	assert.Equal(t, [][]string{{"pr", "close", "690", "--repo", repo, "--comment", "--admin replaced"}}, run.Calls)

	down, _ := failing(t, "HTTP 502")
	err := down.ClosePR(context.Background(), 690, "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot close pull request 690")
}

func TestOpenPulls_ReadsTheAuthorAssociation(t *testing.T) {
	t.Parallel()

	row := func(number int, association string) map[string]any {
		return map[string]any{"number": number, "headRefName": "b", "baseRefName": "main", "authorAssociation": association}
	}

	f, run := fixed(t, toJSON(t, []any{row(1, "OWNER"), row(2, "CONTRIBUTOR"), map[string]any{"number": 3, "headRefName": "b", "baseRefName": "main"}}))

	pulls, err := f.OpenPulls(context.Background(), "")
	require.NoError(t, err)
	require.Len(t, pulls, 3)

	assert.Equal(t, model.AssociationOwner, pulls[0].Association)
	assert.Equal(t, model.AssociationContributor, pulls[1].Association)
	assert.Empty(t, pulls[2].Association, "a row with no association has none, and counts as foreign")
	assert.Contains(t, run.Calls[0], "number,headRefName,baseRefName,labels,reviewDecision,author,authorAssociation,isDraft,title", "the list asks for the association")
}
