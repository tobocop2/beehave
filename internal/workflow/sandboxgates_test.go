package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow"
)

const refusal = `no sandbox can enforce a gate here. Set sandbox = "off"`

func TestCheckSandboxedGates_RefusesEachGateAndBatchMergeCallThatRunsAGateHere(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    gate(name = \"a\")\n    gate(run = [\"true\"], name = \"b\")\n    batch_merge(prs = [], base = \"main\", repo = \"o/r\", name = \"c\")\n    agent(prompt = \"p\", name = \"d\")\n    merge(name = \"e\")\n"}, "workflow.star")

	found := workflow.CheckSandboxedGates(file, "near", refusal, true)

	require.Len(t, found, 3)
	assert.Equal(t, 2, found[0].Line)
	assert.Contains(t, found[0].Text, "gate")
	assert.Contains(t, found[0].Text, "project near")
	assert.Contains(t, found[0].Text, `sandbox = "off"`)
	assert.Equal(t, 3, found[1].Line)
	assert.Equal(t, 4, found[2].Line)
	assert.Contains(t, found[2].Text, "batch_merge")
}

func TestCheckSandboxedGates_AcceptsAProgramGateThatNamesAHost(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    gate(run = [\"true\"], host = \"cluster\", name = \"a\")\n    gate(run = [\"true\"], host = \"\", name = \"b\")\n"}, "workflow.star")

	found := workflow.CheckSandboxedGates(file, "near", refusal, true)

	require.Len(t, found, 1)
	assert.Equal(t, 3, found[0].Line)
}

func TestCheckSandboxedGates_ListsOnlyProgramGatesWhenTheProjectGateRunsOnAHost(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    gate(name = \"a\")\n    gate(run = [\"true\"], name = \"b\")\n    batch_merge(prs = [], base = \"main\", repo = \"o/r\", name = \"c\")\n    batch_merge(prs = [], base = \"main\", repo = \"o/r\", gate_run = [\"x\"], name = \"d\")\n"}, "workflow.star")

	found := workflow.CheckSandboxedGates(file, "far", refusal, false)

	require.Len(t, found, 2)
	assert.Equal(t, 3, found[0].Line)
	assert.Equal(t, 5, found[1].Line)
}

func TestCheckSandboxedGates_IgnoresAFunctionNamedLikeABuiltin(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def gate(name):\n    return name\ndef main(item):\n    gate(name = \"a\")\n"}, "workflow.star")

	assert.Empty(t, workflow.CheckSandboxedGates(file, "near", refusal, true))
}
