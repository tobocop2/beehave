package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/workflow"
)

func list(items ...string) starlark.Value {
	values := make([]starlark.Value, len(items))
	for i, item := range items {
		values[i] = starlark.String(item)
	}

	return starlark.NewList(values)
}

func TestNames_ReadsAListOfDistinctNames(t *testing.T) {
	t.Parallel()

	names, err := workflow.Names("to", list("ana", "bo"))
	require.NoError(t, err)
	assert.Equal(t, []string{"ana", "bo"}, names)
}

func TestNames_RefusesWhatIsNotAListOfDistinctText(t *testing.T) {
	t.Parallel()

	_, err := workflow.Names("to", starlark.MakeInt(1))
	require.ErrorContains(t, err, "to must be a list of strings")

	_, err = workflow.Names("to", list("a", "a"))
	require.ErrorContains(t, err, `to holds "a" twice`)

	_, err = workflow.Names("to", list(" a"))
	require.ErrorContains(t, err, "spaces around it")
}

func TestOptionalNames_AnAbsentKeywordGivesNoNames(t *testing.T) {
	t.Parallel()

	names, err := workflow.OptionalNames("to", nil)
	require.NoError(t, err)
	assert.Empty(t, names)

	names, err = workflow.OptionalNames("to", list("ana"))
	require.NoError(t, err)
	assert.Equal(t, []string{"ana"}, names)
}

func TestQuorum_RefusesWhatIsNotAWholeNumberOfOneOrMore(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]starlark.Value{
		"text": starlark.String("2"), "zero": starlark.MakeInt(0), "negative": starlark.MakeInt(-1), "huge": starlark.MakeInt64(1 << 40),
	} {
		_, err := workflow.Quorum(value)
		require.ErrorContains(t, err, "quorum must be a whole number of 1 or more", name)
	}

	n, err := workflow.Quorum(starlark.MakeInt(3))
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}

func TestCheckQuorum_AQuorumOfZeroIsNoQuorum(t *testing.T) {
	t.Parallel()

	require.NoError(t, workflow.CheckQuorum(0, nil))
	require.NoError(t, workflow.CheckQuorum(2, []string{"a", "b"}))
}

func TestCheck_ALiteralQuorumNeedsALiteralListToBeChecked(t *testing.T) {
	t.Parallel()

	got := checkText(t, "n = 2\n\ndef main(item):\n    wait(question = \"q\", to = [\"a\"], quorum = n)\n    wait(question = \"q\", to = [\"a\"], quorum = 1 + 1)\n")

	assert.Empty(t, got)
}
