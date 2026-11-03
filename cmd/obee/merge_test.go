package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/syntax"

	"github.com/tobocop2/obee/internal/config"
	"github.com/tobocop2/obee/internal/flow"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/workflow/workflowtest"
)

const (
	gated  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	moved  = "ffffffffffffffffffffffffffffffffffffffff"
	number = 5
)

// shipForge is the forge fake of the merge scripts: one open pull request on the branch "item", and the writes it saw.
// A method it does not define panics, so a test shows every call the merge makes.
type shipForge struct {
	forge.Forge

	view     func(call int) model.PullRequest
	views    int
	children []model.PullRequest
	// committers are the committers the forge reports. Nil means one unnamed committer, as the pull request has one commit.
	committers []model.Committer
	writes     []string
	// tipTree is the tree of the tip of the base after the merge.
	tipTree string
}

func (f *shipForge) PRList(_ context.Context, _ int, args ...string) ([]model.PullRequest, error) {
	for i, arg := range args {
		switch {
		case arg == "--state" && args[i+1] == "merged":
			return nil, nil
		case arg == "--head" && args[i+1] != "item" && args[i+1] != "12":
			return nil, nil
		case arg == "--head":
			return []model.PullRequest{{Number: number}}, nil
		case arg == "--base":
			return f.children, nil
		}
	}

	return nil, nil
}

func (f *shipForge) PRView(context.Context, int, ...string) (model.PullRequest, error) {
	f.views++

	return f.view(f.views), nil
}

func (*shipForge) PullCounts(context.Context, int) (commits, files int, err error) { return 1, 1, nil }

func (f *shipForge) Committers(context.Context, int) ([]model.Committer, error) {
	if f.committers == nil {
		return []model.Committer{{}}, nil
	}

	return f.committers, nil
}

func (*shipForge) PRChecks(context.Context, int) ([]model.Check, error) {
	return []model.Check{{Name: "ci", Bucket: model.CheckPass}}, nil
}

func (*shipForge) OpenChangeRequests(context.Context, int) ([]model.ChangeRequest, error) {
	return nil, nil
}

func (*shipForge) UnresolvedThreads(context.Context, int) ([]model.ReviewThread, error) {
	return nil, nil
}

func (*shipForge) StackID(context.Context, int) (string, error) { return "", nil }

func (*shipForge) Reviews(context.Context, int) ([]model.Review, error) { return nil, nil }

func (*shipForge) IsAncestor(context.Context, string, string) (bool, error) { return true, nil }

func (*shipForge) TokenScopes(context.Context) ([]string, error) { return []string{"repo"}, nil }

func (*shipForge) IssueState(context.Context, int) (string, error) {
	return "", errFake
}

func (*shipForge) ClosingRefs(context.Context, int) (model.ClosingRefs, error) {
	return model.ClosingRefs{}, errFake
}

func (*shipForge) BranchRef(context.Context, string) (string, bool, error) {
	return strings.Repeat("c", 40), true, nil
}

// CommitTree gives the tree of the tip of the base after the merge: tipTree. Any other commit has the tree "gated-tree".
func (f *shipForge) CommitTree(_ context.Context, commit string) (string, error) {
	if commit == strings.Repeat("c", 40) {
		return f.tipTree, nil
	}

	return "gated-tree", nil
}

// TreeBlobs lists one file for the gated tree and one more for any other tree.
func (*shipForge) TreeBlobs(_ context.Context, tree string) (map[string]string, error) {
	if tree == "gated-tree" {
		return map[string]string{"a.go": "1"}, nil
	}

	return map[string]string{"a.go": "2"}, nil
}

func (f *shipForge) SetBase(_ context.Context, pr int, base string) error {
	f.writes = append(f.writes, fmt.Sprintf("SetBase %d %s", pr, base))

	return nil
}

func (f *shipForge) MergePR(_ context.Context, pr int, sha string) error {
	f.writes = append(f.writes, fmt.Sprintf("MergePR %d %s", pr, sha))

	return nil
}

// pullOf decodes a pull request of the default shape with the keys of over replaced.
func pullOf(t *testing.T, over map[string]any) model.PullRequest {
	t.Helper()

	row := map[string]any{
		"number": number, "state": "OPEN", "isDraft": false, "baseRefName": "integration", "headRefName": "item",
		"headRefOid": gated, "mergeable": "MERGEABLE", "body": "text", "author": map[string]any{"login": "dev"},
		"files":   []any{map[string]any{"path": "a.go"}},
		"commits": []any{commit("dev@x", "dev", "fix", "")},
	}
	for key, value := range over {
		row[key] = value
	}

	text, err := json.Marshal(row)
	require.NoError(t, err)

	view, err := model.Decode[model.PullRequest]("test", string(text))
	require.NoError(t, err)

	return view
}

func commit(email, login, headline, body string) map[string]any {
	author := map[string]any{"email": email, "login": login}

	return map[string]any{"oid": strings.Repeat("b", 40), "messageHeadline": headline, "messageBody": body, "authors": []any{author}}
}

// shipping returns the pulls over a forge whose pull request has the keys of over replaced, with the settings of the example.
func shipping(t *testing.T, over map[string]any) (pulls, *shipForge, flow.MergeSettings) {
	t.Helper()

	f := &shipForge{}
	view := pullOf(t, over)
	f.view = func(int) model.PullRequest { return view }

	p := pulls{
		projects: map[string]config.Project{"p": {Project: model.Project{Repo: "o/r"}}},
		forge:    func(model.Project) (forge.Forge, error) { return f, nil },
		runners:  []string{"bee-1"},
	}

	return p, f, flow.MergeSettings{
		Repo: "o/r", Base: "integration", Hold: "hold", Logins: []string{"me"}, Emails: []string{"me@x"},
		ForbidText: exampleForbidText(t), Retarget: true,
	}
}

// exampleForbidText reads the `forbid_text` patterns of the merge step of the remediation example.
func exampleForbidText(t *testing.T) []string {
	t.Helper()

	var patterns []string

	walkExampleMerge(t, func(ship map[string]syntax.Expr) {
		patterns = append(patterns, workflowtest.Literals(ship["forbid_text"])...)
	})

	return patterns
}

func walkExampleMerge(t *testing.T, visit func(map[string]syntax.Expr)) {
	t.Helper()

	text, err := os.ReadFile("../../examples/remediation/workflow.star")
	require.NoError(t, err)

	calls, err := workflowtest.Calls("workflow.star", text)
	require.NoError(t, err)

	for _, call := range calls {
		if call.Builtin == "merge" {
			visit(call.Args)
		}
	}
}

func TestMerge_AGatedTreeReachesTheMergeAndATipWithAnotherTreeStopsTheRunWithTheDifferingPaths(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	settings.GatedTree = "gated-tree"
	f.tipTree = "other-tree"

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, strings.Join(result.Lines, "\n"), "a.go")
	assert.Equal(t, []string{"MergePR 5 " + gated}, f.writes, "the merge happened, and the stop comes after it")
}

func TestMerge_AGatedTreeThatTheTipHoldsPasses(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	settings.GatedTree = "gated-tree"
	f.tipTree = "gated-tree"

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Done, result.Stop, strings.Join(result.Lines, "\n"))
}

type noTreeForge struct{ forge.Forge }

func TestOpened_AForgeWithoutTreesOrLogsGivesAnErrorNotAPanic(t *testing.T) {
	t.Parallel()

	none := opened{noTreeForge{}}

	_, treeErr := none.CommitTree(t.Context(), "x")
	_, blobErr := none.TreeBlobs(t.Context(), "x")
	_, logErr := none.FailedLog(t.Context(), "x")

	for _, err := range []error{treeErr, blobErr, logErr} {
		require.ErrorIs(t, err, errNoOptionalRead)
	}
}

type historyForge struct{ forge.Forge }

func (historyForge) IsAncestor(context.Context, string, string) (bool, error) { return true, nil }

func TestOpened_AncestryIsPassedThroughOrGivesAnError(t *testing.T) {
	t.Parallel()

	_, err := opened{noTreeForge{}}.IsAncestor(t.Context(), "x", "main")
	require.ErrorIs(t, err, errNoOptionalRead)

	in, err := opened{historyForge{}}.IsAncestor(t.Context(), "x", "main")
	require.NoError(t, err)
	assert.True(t, in)
}

type queueForge struct {
	forge.Forge
	called string
}

func (q *queueForge) AutoMergePR(_ context.Context, pr int, sha string) error {
	q.called = fmt.Sprintf("%d %s", pr, sha)

	return nil
}

func (q *queueForge) DisableAutoMergePR(_ context.Context, pr int) error {
	q.called = fmt.Sprintf("off %d", pr)

	return nil
}

func TestPulls_PollJudgesTheQueuedMergeWithTheRequestOfTheMerge(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)

	result, err := p.Poll(t.Context(), "p", "item", gated, settings, true)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, strings.Join(result.Lines, "\n"), "may still be armed", "the last read of an open pull request disarms, and this forge cannot")
	assert.Empty(t, f.writes)
}

func TestOpened_ADisableIsPassedThroughOrGivesAnError(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, opened{noTreeForge{}}.DisableAutoMergePR(t.Context(), 5), errNoOptionalRead)

	queue := &queueForge{}

	require.NoError(t, opened{queue}.DisableAutoMergePR(t.Context(), 5))
	assert.Equal(t, "off 5", queue.called)
}

func TestOpened_AQueuedMergeIsPassedThroughOrGivesAnError(t *testing.T) {
	t.Parallel()

	err := opened{noTreeForge{}}.AutoMergePR(t.Context(), 5, "x")
	require.ErrorIs(t, err, errNoOptionalRead)

	queue := &queueForge{}

	require.NoError(t, opened{queue}.AutoMergePR(t.Context(), 5, "x"))
	assert.Equal(t, "5 x", queue.called)
}

type logForge struct{ forge.Forge }

func (logForge) FailedLog(context.Context, string) (string, error) { return "log text", nil }

func TestOpened_ALogOfTheForgeIsPassedThrough(t *testing.T) {
	t.Parallel()

	text, err := opened{logForge{}}.FailedLog(t.Context(), "link")

	require.NoError(t, err)
	assert.Equal(t, "log text", text)
}

func TestMerge_M13_OwnWorkWithoutApprovedIsRefusedWithNoWriteCall(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, map[string]any{"commits": []any{commit("me@x", "linked", "fix", "")}})

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.OwnWork, result.Stop)
	assert.Empty(t, f.writes)
}

func TestMerge_M13_OwnWorkWithApprovedIsMergedAtThePin(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, map[string]any{"commits": []any{commit("me@x", "linked", "fix", "")}})
	settings.Approved = true

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Done, result.Stop, strings.Join(result.Lines, "\n"))
	assert.Equal(t, []string{"MergePR 5 " + gated}, f.writes)
}

func TestMerge_ARunnerOfTheConfigIsAnOwnIdentity(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, map[string]any{"author": map[string]any{"login": "bee-1"}})

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.OwnWork, result.Stop)
	assert.Empty(t, f.writes)
}

func TestMerge_M31_AHeldPullRequestIsNotMerged(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, map[string]any{"labels": []any{map[string]any{"name": "hold"}}})

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, strings.Join(result.Lines, "\n"), "has the label hold")
	assert.Empty(t, f.writes)
}

func TestMerge_M08_ABodyWithAnAttributionLineIsRefusedWithItsLineNumber(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, map[string]any{"body": "a\nb\nGenerated with [Claude Code]"})

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, "  3:Generated with [Claude Code]", result.Lines[len(result.Lines)-1])
	assert.Empty(t, f.writes)
}

func TestMerge_M19_C18_AHeadThatMovesBeforeTheMergeCallIsRefusedAndNothingIsMerged(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	f.view = func(call int) model.PullRequest {
		if call > 1 {
			return pullOf(t, map[string]any{"headRefOid": moved})
		}

		return pullOf(t, nil)
	}

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Empty(t, f.writes)
}

func TestMerge_C19_ChildrenRetargetMovesTheChildFirstAndRefuseBlocks(t *testing.T) {
	t.Parallel()

	child := pullOf(t, map[string]any{"number": 7, "baseRefName": "item"})

	p, f, settings := shipping(t, nil)
	f.children = []model.PullRequest{child}

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)
	require.NoError(t, err)
	assert.Equal(t, merge.Done, result.Stop, strings.Join(result.Lines, "\n"))
	assert.Equal(t, []string{"SetBase 7 integration", "MergePR 5 " + gated}, f.writes)

	p, f, settings = shipping(t, nil)
	f.children = []model.PullRequest{child}
	settings.Retarget = false

	result, err = p.Merge(t.Context(), "p", "item", gated, settings)
	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Empty(t, f.writes)
}

func TestMerge_ARepoThatIsNotTheRepositoryOfTheProjectIsRefusedAndNothingIsRead(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	settings.Repo = "someone/else"

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Refused, result.Stop)
	assert.Equal(t, []string{"REFUSE: the merge step names the repository someone/else, and the project p uses o/r. Set `repo` to o/r."}, result.Lines)
	assert.Zero(t, f.views)
	assert.Empty(t, f.writes)
}

func TestMerge_TheRepoOfTheProjectMatchesWithoutRegardToCase(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	settings.Repo = "O/R"

	result, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.NoError(t, err)
	assert.Equal(t, merge.Done, result.Stop, strings.Join(result.Lines, "\n"))
	assert.Equal(t, []string{"MergePR 5 " + gated}, f.writes)
}

func TestMerge_APatternThatIsNotValidIsAnErrorAndNothingIsRead(t *testing.T) {
	t.Parallel()

	p, f, settings := shipping(t, nil)
	settings.ForbidText = []string{"(unclosed"}

	_, err := p.Merge(t.Context(), "p", "item", gated, settings)

	require.ErrorContains(t, err, "(unclosed")
	assert.Zero(t, f.views)
}

func TestMerge_AnItemWithNoPullRequestAndAProjectWithNoForgeAreErrors(t *testing.T) {
	t.Parallel()

	p, _, settings := shipping(t, nil)
	p.forge = func(model.Project) (forge.Forge, error) { return &shipForge{}, nil }

	_, err := p.Merge(t.Context(), "p", "other", gated, settings)
	require.Error(t, err)

	p = pulls{projects: map[string]config.Project{"p": {Project: model.Project{Name: "p"}}}, forge: newAdapters(nil, nil).forge}

	_, err = p.Merge(t.Context(), "p", "item", gated, settings)
	require.ErrorIs(t, err, forge.ErrNoForge)
}

func TestNewLife_TheRunnersOfTheConfigAreTheOwnRunnersOfTheMerger(t *testing.T) {
	t.Parallel()

	life := newLife(config.Config{Runners: []model.Runner{{Name: "bee-1"}, {Name: "bee-2"}}}, newAdapters(nil, nil))

	assert.Equal(t, []string{"bee-1", "bee-2"}, life.runners)
}

func TestMerge_TheValuesRowTheExampleHoldsEverySettingAndNoIdentity(t *testing.T) {
	t.Parallel()

	var seen bool

	walkExampleMerge(t, func(ship map[string]syntax.Expr) {
		seen = true

		for _, name := range []string{"repo", "base", "hold", "own", "approved", "forbid_text", "children"} {
			assert.NotNil(t, ship[name], "%s is set in the example", name)
		}

		assert.NotNil(t, ship["tree"], "the example passes the tree of the gate, so a merge proves the merged tree")

		assert.Equal(t, []string{"BASE"}, workflowtest.Literals(ship["base"]), "base is the integration branch, read from the environment")
		assert.Equal(t, []string{"REPO"}, workflowtest.Literals(ship["repo"]))
		assert.Equal(t, []string{"retarget"}, workflowtest.Literals(ship["children"]))
		assert.Equal(t, []string{"hold"}, workflowtest.Literals(ship["hold"]))

		for _, text := range workflowtest.Literals(ship["own"]) {
			assert.NotContains(t, text, "@", "the file holds no address")
		}

		assert.Equal(t, []string{"approve"}, workflowtest.Literals(ship["approved"]))
	})
	assert.True(t, seen)
}
