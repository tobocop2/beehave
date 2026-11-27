package workflow_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehest/internal/workflow"
)

func TestValues_NoneAndAnAbsentArgumentAreEmpty(t *testing.T) {
	t.Parallel()

	dict, ok := workflow.Optional[*starlark.Dict](starlark.None)
	require.True(t, ok)
	assert.Nil(t, dict)

	items, ok := workflow.Elements(starlark.None)
	assert.True(t, ok)
	assert.Empty(t, items)

	_, ok = workflow.Elements(starlark.MakeInt(1))
	assert.False(t, ok)

	_, err := workflow.ListOfEach(starlark.MakeInt(1), func(starlark.Value) (int, error) { return 0, nil }, assert.AnError)
	require.ErrorIs(t, err, assert.AnError)

	limit, err := workflow.ProcessCap(nil, "host")
	require.NoError(t, err)
	assert.Zero(t, limit)

	class, err := workflow.SlotClass(nil, "")
	require.NoError(t, err)
	assert.Empty(t, class, "a call with no slots keyword has no class, with or without a host")

	_, globs, err := workflow.AllowList("allow_paths", starlark.None)
	require.NoError(t, err)
	assert.Empty(t, globs)

	data, err := workflow.InputMap(nil)
	require.NoError(t, err)
	assert.Empty(t, data)

	_, err = workflow.InputMap(starlark.MakeInt(1))
	require.ErrorContains(t, err, "inputs must be a dict, not a int")
}

func TestValues_ToGoKeepsNumbersAndRefusesAWrongType(t *testing.T) {
	t.Parallel()

	_, err := workflow.ToGo[map[string]any](starlark.NewList(nil))
	require.EqualError(t, err, "a list is not the kind of value this place takes")

	n, err := workflow.ToGo[any](starlark.MakeInt64(1).Lsh(70))
	require.NoError(t, err)
	assert.Equal(t, json.Number("1180591620717411303424"), n)

	_, err = workflow.ToGo[any](starlark.Float(math.NaN()))
	require.ErrorContains(t, err, "cannot go into a prompt or a request")
}

func TestCheck_ReadsEveryKindOfLiteralBeforeApplyingTheRulesOfTheRun(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, call, want string }{
		{"float element", `agent("p", "m", outputs = [1.5])`, "outputs must hold strings only"},
		{"big element", `agent("p", "m", outputs = [100000000000000000000])`, "outputs must hold strings only"},
		{"negative float element", `agent("p", "m", outputs = [-1.5])`, "outputs must hold strings only"},
		{"negative big", `agent("p", "m", output_tries = -100000000000000000000)`, "output_tries\": -100000000000000000000 out of range"},
		{"tuple of False", `agent("p", "m", outputs = (False,))`, "outputs must hold strings only"},
		{"parenthesis and None", `agent("p", "m", outputs = ([None]))`, "outputs must hold strings only"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    "+tc.call+"\n")

			require.NotEmpty(t, got)
			assert.Contains(t, got[0], tc.want)
		})
	}
}

func TestCheck_ABytesLiteralIsBytesAndNotTheTextItSpells(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    agent(\"p\", \"m\", outputs = [b\"x\"])\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "outputs must hold strings only, and b\"x\" is a bytes")
}

func TestCheck_LeavesAValueThatIsNotAWholeLiteralToTheRun(t *testing.T) {
	t.Parallel()

	got := checkText(t, "N = 0\n\ndef main(item):\n"+
		"    agent(\"p\", \"m\", output_tries = N, outputs = [\"a\", N], allow_paths = [\"a\"] + [\"[\"], inputs = {N: 1}, timeout = N)\n"+
		"    agent(\"p\", \"m\", outputs = [-N],inputs = {\"a\": N})\n"+
		"    agent(\"p\", \"m\", output_tries = 5, protect_paths = [\"a\"], forbid_commands = [\"protected-path\"])\n"+
		"    command(run = [\"x\"], host = N, max_new_processes = 5)\n"+
		"    command(run = [\"x\"], max_new_processes = 5)\n")

	assert.Empty(t, got)
}

func TestValues_AllowListRefusesTheListsThatCannotMatchAPathOfTheItemFolderAndNamesThePattern(t *testing.T) {
	t.Parallel()

	list := func(items ...string) starlark.Value {
		values := make([]starlark.Value, len(items))
		for i, item := range items {
			values[i] = starlark.String(item)
		}

		return starlark.NewList(values)
	}

	for name, c := range map[string]struct {
		value   starlark.Value
		message string
	}{
		"an empty entry":         {list("src/**", ""), `allow has the empty pattern ""`},
		"a dot slash only":       {list("./"), `allow has the empty pattern "./"`},
		"an absolute path":       {list("/docs/**"), `allow has the absolute pattern "/docs/**"`},
		"a path that leaves":     {list("../docs"), `allow has the pattern "../docs", which leaves the item folder`},
		"a double dot":           {list(".."), `allow has the pattern "..", which leaves the item folder`},
		"an inner double dot":    {list("docs/../x"), `allow has the pattern "docs/../x", which leaves the item folder`},
		"a trailing double dot":  {list("docs/.."), `allow has the pattern "docs/..", which leaves the item folder`},
		"a brace choice of ..":   {list("{docs,..}/x"), `allow has the pattern "{docs,..}/x", which leaves the item folder`},
		"a nested brace of ..":   {list("{a,{b,..}}/x"), `allow has the pattern "{a,{b,..}}/x", which leaves the item folder`},
		"a brace that spells ..": {list("{docs,.}./x"), `allow has the pattern "{docs,.}./x", which leaves the item folder`},
		"a brace after a class":  {list("[{]/{a,..}/x"), `allow has the pattern "[{]/{a,..}/x", which leaves the item folder`},
		"a class with an escape": {list(`[\]]/{a,..}/x`), `allow has the pattern "[\\]]/{a,..}/x", which leaves the item folder`},
		"an escaped brace":       {list(`\{a,..}/x`), `allow has the pattern "\\{a,..}/x", which does not parse`},
		"a brace that is open":   {list("{docs,../x"), `allow has the pattern "{docs,../x", which does not parse`},
		"a bad bracket":          {list("docs/["), `allow has the pattern "docs/[", which does not parse`},
		"an empty list":          {list(), "allow is an empty list, which would allow every path"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, _, err := workflow.AllowList("allow", c.value)

			require.ErrorContains(t, err, c.message)
		})
	}

	patterns, globs, err := workflow.AllowList("allow", list("./src/**", "{docs,pkg}/*.md"))

	require.NoError(t, err)
	assert.Equal(t, []string{"./src/**", "{docs,pkg}/*.md"}, patterns)
	assert.Equal(t, []string{"src/**", "{docs,pkg}/*.md"}, globs, "the dot slash is dropped and the braces stay")
	assert.True(t, workflow.AllowsPath(globs, "docs/a.md"))
	assert.True(t, workflow.AllowsPath(globs, "src/a/b.go"), "a double star crosses the slash")
	assert.False(t, workflow.AllowsPath(globs, "docs/a/b.md"), "a single star stops at the slash")

	_, _, err = workflow.AllowList("allow", list("docs/a..b/**", "CHANGELOG..md", "..hidden/**"))
	require.NoError(t, err, "two dots inside one part are a plain name")

	_, _, err = workflow.AllowList("allow", list("{a..b,c}/**", `{a\,..,b}/x`, "{a,{b,c}}/x"))
	require.NoError(t, err, "a brace choice with no .. part, and a comma that a backslash escapes, are plain choices")
	assert.False(t, workflow.AllowsPath(globs, "other.go"))
}

func TestDataRecord_EncodesADictOnceAsJSONAndNoneAsNoRecord(t *testing.T) {
	t.Parallel()

	dict := starlark.NewDict(2)
	require.NoError(t, dict.SetKey(starlark.String("reds"), starlark.NewList([]starlark.Value{starlark.String("lint-a")})))
	require.NoError(t, dict.SetKey(starlark.String("base"), starlark.String("main")))

	text, err := workflow.DataRecord(dict)
	require.NoError(t, err)
	assert.JSONEq(t, `{"base":"main","reds":["lint-a"]}`, text)

	for name, v := range map[string]starlark.Value{"absent": nil, "None": starlark.None} {
		text, err = workflow.DataRecord(v)
		require.NoError(t, err, name)
		assert.Empty(t, text, name)
	}

	_, err = workflow.DataRecord(starlark.NewList(nil))
	require.ErrorContains(t, err, "data must be a dict, not a list")

	bad := starlark.NewDict(1)
	require.NoError(t, bad.SetKey(starlark.String("x"), starlark.Float(math.NaN())))
	_, err = workflow.DataRecord(bad)
	require.ErrorContains(t, err, "a value cannot go into a prompt or a request")
}
