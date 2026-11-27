package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"

	"github.com/tobocop2/beehest/internal/workflow"
)

func TestMembers_ReadsTheMembersOfAUnionInOrderAndNamesThePlaceOfABadOne(t *testing.T) {
	t.Parallel()

	member := func(item string) starlark.Value {
		return starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
			"item": starlark.String(item), "head": starlark.String("h" + item), "approved": starlark.True, "closes": starlark.NewList([]starlark.Value{starlark.MakeInt(3)}),
		})
	}

	got, err := workflow.Members(starlark.NewList([]starlark.Value{member("7"), member("2")}))

	require.NoError(t, err)
	assert.Equal(t, []workflow.PullRequest{{Item: "7", Head: "h7", Approved: true, Closes: []int{3}}, {Item: "2", Head: "h2", Approved: true, Closes: []int{3}}}, got)

	cases := map[string]struct {
		value starlark.Value
		want  string
	}{
		"not a list":    {starlark.String("7"), "`members` must be a list"},
		"an empty list": {starlark.NewList(nil), "`members` is empty"},
		"a bad member":  {starlark.NewList([]starlark.Value{member("7"), starlark.String("2")}), "members[1]: the member has the type string, not struct"},
		"an item twice": {starlark.NewList([]starlark.Value{member("7"), member("7")}), "`members` names item 7 twice"},
	}

	for name, c := range cases {
		_, err := workflow.Members(c.value)
		require.ErrorContains(t, err, c.want, name)
	}
}

func TestCheck_AMergeOrAHandOffCallIsCheckedForItsKeywords(t *testing.T) {
	t.Parallel()

	const member = `struct(item = "1", head = "h", closes = [])`

	cases := map[string]struct{ call, want string }{
		"merge with member":         {`merge(member = ` + member + `, base = "b", repo = "r", name = "n")`, "merge takes no argument member"},
		"merge with gated_tree":     {`merge(members = [` + member + `], base = "b", repo = "r", gate = g, gated_tree = g.tree, name = "n")`, "merge takes no argument gated_tree"},
		"merge with expect_tree":    {`merge(members = [` + member + `], base = "b", repo = "r", expect_tree = "t", name = "n")`, "merge takes no argument expect_tree"},
		"hand_off with a landing":   {`hand_off(prs = [` + member + `], base = "b", repo = "r", hold = "x", name = "n")`, "hand_off takes no argument hold. Use one of: prs, base, repo, name."},
		"hand_off with no name":     {`hand_off(prs = [` + member + `], base = "b", repo = "r")`, "hand_off needs the `name` keyword"},
		"hand_off with empty prs":   {`hand_off(prs = [], base = "b", repo = "r", name = "n")`, "`prs` is empty"},
		"hand_off result bad field": {`hand_off(prs = [` + member + `], base = "b", repo = "r", name = "n").tree2`, "tree2 is not a field of the result of hand_off"},
		"merge result bad field":    {`merge(members = [` + member + `], base = "b", repo = "r", gate = g, name = "n").status`, "status is not a field of the result of merge"},
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
		"a union with a gate":    `print(merge(members = [` + member + `], base = "b", repo = "r", gate = g, name = "n").tip)`,
		"a union with no gate":   `merge(members = [` + member + `], base = "b", repo = "r", name = "n")`,
		"a union with gate None": `merge(members = [` + member + `], base = "b", repo = "r", gate = None, name = "n")`,
		"a hand-off":             `hand_off(prs = [` + member + `], base = "b", repo = "r", name = "n")`,
	} {
		assert.Empty(t, checkText(t, "def main(item):\n    g = gate(name = \"g\")\n    "+call+"\n"), name)
	}
}
