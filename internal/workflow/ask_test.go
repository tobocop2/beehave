package workflow_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/beehave/internal/workflow"
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

func TestRemind_ReadsAListOrATupleOfSharesInOrder(t *testing.T) {
	t.Parallel()

	shares, err := workflow.Remind(starlark.NewList([]starlark.Value{starlark.Float(0.5), starlark.Float(0.9)}))
	require.NoError(t, err)
	assert.Equal(t, []float64{0.5, 0.9}, shares)

	shares, err = workflow.Remind(starlark.Tuple{starlark.Float(0.25)})
	require.NoError(t, err)
	assert.Equal(t, []float64{0.25}, shares)
}

func TestRemind_RefusesWhatIsNotAnOrderedListOfSharesBelowOne(t *testing.T) {
	t.Parallel()

	_, err := workflow.Remind(starlark.Float(0.5))
	require.ErrorContains(t, err, "remind must be a list of shares of the window")

	_, err = workflow.Remind(starlark.NewList([]starlark.Value{starlark.Float(math.NaN())}))
	require.ErrorContains(t, err, "not a share of the window")

	_, err = workflow.Remind(starlark.NewList([]starlark.Value{starlark.MakeInt(1)}))
	require.ErrorContains(t, err, "remind holds 1, which is not a share of the window")
}

func TestCheckRemind_NeedsAWindowAndPeople(t *testing.T) {
	t.Parallel()

	require.NoError(t, workflow.CheckRemind(true, true))
	require.ErrorContains(t, workflow.CheckRemind(false, true), "remind needs `timeout`")
	require.ErrorContains(t, workflow.CheckRemind(true, false), "remind needs `to`")
}

func TestCheck_ALiteralQuorumNeedsALiteralListToBeChecked(t *testing.T) {
	t.Parallel()

	got := checkText(t, "n = 2\n\ndef main(item):\n    wait(question = \"q\", to = [\"a\"], quorum = n)\n    wait(question = \"q\", to = [\"a\"], quorum = 1 + 1)\n")

	assert.Empty(t, got)
}
