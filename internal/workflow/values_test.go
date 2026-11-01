package workflow_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/workflow"
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

	_, globs, err := workflow.AllowPaths(starlark.None)
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
		{"negative big", `agent("p", "m", output_tries = -100000000000000000000)`, "output_tries must be a whole number of 1 or more"},
		{"tuple of False", `agent("p", "m", outputs = (False,))`, "outputs must hold strings only"},
		{"parenthesis and None", `agent("p", "m", outputs = ([None]))`, "outputs must hold strings only"},
		{"negative in a keyword with a closed set", `agent("p", "m", follow_ups = -1)`, "follow_ups is -1"},
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

func TestCheck_LeavesAValueThatIsNotAWholeLiteralToTheRun(t *testing.T) {
	t.Parallel()

	got := checkText(t, "N = 0\n\ndef main(item):\n"+
		"    agent(\"p\", \"m\", output_tries = N, outputs = [\"a\", N], allow_paths = [\"a\"] + [\"[\"], inputs = {N: 1}, timeout = N)\n"+
		"    agent(\"p\", \"m\", outputs = [b\"x\"])\n"+
		"    agent(\"p\", \"m\", outputs = [-N],inputs = {\"a\": N}, require_skills = [not True])\n"+
		"    agent(\"p\", \"m\", output_tries = 100000000000000000000, protect_paths = [\"a\"], forbid_commands = [\"protected-path\"])\n"+
		"    command(run = [\"x\"], host = N, max_new_processes = 5)\n"+
		"    command(run = [\"x\"], max_new_processes = 5)\n")

	assert.Empty(t, got)
}
