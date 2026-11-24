package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehave/internal/workflow"
)

func TestMergeForm_NamesTheKeywordThatAFormDoesNotTake(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		keywords workflow.MergeKeywords
		want     string
	}{
		"no prs and no member":           {workflow.MergeKeywords{}, "merge needs `prs` or `member`"},
		"prs and member":                 {workflow.MergeKeywords{Prs: true, Member: true}, "merge takes `prs` or `member`, not both"},
		"member with gate_run":           {workflow.MergeKeywords{Member: true, GateRun: true}, "merge with `member` lands one pull request, so it takes no `gate_run`"},
		"member with integration":        {workflow.MergeKeywords{Member: true, Integration: true}, "takes no `integration`"},
		"member with hand_off":           {workflow.MergeKeywords{Member: true, HandOff: true}, "takes no `hand_off`"},
		"member with a gate and no tree": {workflow.MergeKeywords{Member: true, Gate: true}, "merge with `gate` needs `gated_tree`"},
		"member with a tree and no gate": {workflow.MergeKeywords{Member: true, GatedTree: true}, "merge with `gated_tree` needs `gate`"},
		"prs with gate":                  {workflow.MergeKeywords{Prs: true, Gate: true}, "merge with `prs` runs its own gate, so it takes no `gate`"},
		"prs with gated_tree":            {workflow.MergeKeywords{Prs: true, GatedTree: true}, "takes no `gated_tree`"},
		"prs with expect_tree":           {workflow.MergeKeywords{Prs: true, ExpectTree: true}, "takes no `expect_tree`"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.ErrorContains(t, workflow.MergeForm(c.keywords), c.want)
		})
	}

	for name, keywords := range map[string]workflow.MergeKeywords{
		"a list":                        {Prs: true, GateRun: true, Integration: true, HandOff: true},
		"a member with a gate and tree": {Member: true, Gate: true, GatedTree: true, ExpectTree: true},
		"a member with no gate":         {Member: true, ExpectTree: true},
	} {
		assert.NoError(t, workflow.MergeForm(keywords), name)
	}
}

func TestMember_ReadsTheFieldsOfAMemberOfPrsAndNamesTheKeywordInAnError(t *testing.T) {
	t.Parallel()

	member := starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"item": starlark.String("7"), "head": starlark.String("abc"), "approved": starlark.True, "closes": starlark.NewList([]starlark.Value{starlark.MakeInt(3)}),
	})

	got, err := workflow.Member(member)

	require.NoError(t, err)
	assert.Equal(t, workflow.PullRequest{Item: "7", Head: "abc", Approved: true, Closes: []int{3}}, got)

	_, err = workflow.Member(starlark.String("7"))

	require.ErrorContains(t, err, "member: the member has the type string, not struct")
}

func TestCheck_AMergeCallIsCheckedForTheKeywordsOfItsForm(t *testing.T) {
	t.Parallel()

	const member = `struct(item = "1", head = "h", closes = [])`

	cases := map[string]struct{ call, want string }{
		"member with gate_run":      {`merge(member = ` + member + `, base = "b", repo = "r", gate_run = ["x"], name = "n")`, "merge with `member` lands one pull request, so it takes no `gate_run`"},
		"member with hand_off":      {`merge(member = ` + member + `, base = "b", repo = "r", hand_off = True, name = "n")`, "takes no `hand_off`"},
		"gate with no gated_tree":   {`merge(member = ` + member + `, base = "b", repo = "r", gate = g, name = "n")`, "merge with `gate` needs `gated_tree`"},
		"prs with gate":             {`merge(prs = [` + member + `], base = "b", repo = "r", gate = g, name = "n")`, "merge with `prs` runs its own gate, so it takes no `gate`"},
		"neither prs nor member":    {`merge(base = "b", repo = "r", name = "n")`, "merge needs `prs` or `member`"},
		"prs and member":            {`merge(prs = [` + member + `], member = ` + member + `, base = "b", repo = "r", name = "n")`, "merge takes `prs` or `member`, not both"},
		"hand_off with a landing":   {`hand_off(prs = [` + member + `], base = "b", repo = "r", hold = "x", name = "n")`, "hand_off takes no argument hold. Use one of: prs, base, repo, name."},
		"hand_off with no name":     {`hand_off(prs = [` + member + `], base = "b", repo = "r")`, "hand_off needs the `name` keyword"},
		"hand_off with empty prs":   {`hand_off(prs = [], base = "b", repo = "r", name = "n")`, "`prs` is empty"},
		"hand_off result bad field": {`hand_off(prs = [` + member + `], base = "b", repo = "r", name = "n").tree2`, "tree2 is not a field of the result of hand_off"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    g = gate(name = \"g\")\n    "+c.call+"\n")

			require.NotEmpty(t, got)
			assert.Contains(t, got[0], c.want)
		})
	}

	for name, call := range map[string]string{
		"a member call with a gate":    `merge(member = ` + member + `, base = "b", repo = "r", gate = g, gated_tree = g.tree, expect_tree = "t", name = "n")`,
		"a member call with no gate":   `merge(member = ` + member + `, base = "b", repo = "r", name = "n")`,
		"a hand-off":                   `hand_off(prs = [` + member + `], base = "b", repo = "r", name = "n")`,
		"a list with a computed flag":  `merge(prs = [` + member + `], base = "b", repo = "r", hand_off = item.id == "1", name = "n")`,
		"a member call with gate None": `merge(member = ` + member + `, base = "b", repo = "r", gate = None, name = "n")`,
	} {
		assert.Empty(t, checkText(t, "def main(item):\n    g = gate(name = \"g\")\n    "+call+"\n"), name)
	}
}
