package workflowtest_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow/workflowtest"
)

func TestCalls_ListsTheBuiltinCallsWithTheirKeywordArguments(t *testing.T) {
	t.Parallel()

	calls, err := workflowtest.Calls("workflow.star", []byte(`def main(item):
    helper(name = "not a builtin")
    if item:
        command(run = ["remedy", "cut"], name = "cut")
    other.command(name = "a method")
    gate("positional", name = "check")
`))

	require.NoError(t, err)
	require.Len(t, calls, 2)

	assert.Equal(t, "command", calls[0].Builtin)
	assert.Equal(t, []string{"remedy", "cut"}, workflowtest.Literals(calls[0].Args["run"]))
	assert.Equal(t, []string{"cut"}, workflowtest.Literals(calls[0].Args["name"]))
	assert.Equal(t, "gate", calls[1].Builtin)
	assert.Empty(t, workflowtest.Literals(calls[1].Args["run"]), "an argument the call leaves out has no text")
	assert.Equal(t, []string{"check"}, workflowtest.Literals(calls[1].Args["name"]))
}

func TestCalls_RefusesTextThatDoesNotParse(t *testing.T) {
	t.Parallel()

	_, err := workflowtest.Calls("workflow.star", []byte("def main(item:\n"))

	require.Error(t, err)
}

func TestLiterals_SkipsNumbersAndNames(t *testing.T) {
	t.Parallel()

	calls, err := workflowtest.Calls("workflow.star", []byte(`command(run = [1, "a", x, "b"], stop_on_rc = [2])`))

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b"}, workflowtest.Literals(calls[0].Args["run"]))
	assert.Empty(t, workflowtest.Literals(calls[0].Args["stop_on_rc"]))
}
