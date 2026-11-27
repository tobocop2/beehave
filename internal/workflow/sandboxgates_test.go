package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/workflow"
)

const refusal = `no sandbox can enforce a gate here. Set sandbox = "off"`

func TestCheckSandboxedGates_RefusesEachGateAndMergeCallThatRunsAGateHere(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    gate(name = \"a\")\n    gate(run = [\"true\"], name = \"b\")\n    merge(members = [], base = \"main\", repo = \"o/r\", name = \"c\")\n    agent(prompt = \"p\", name = \"d\")\n"}, "workflow.star")

	found := workflow.CheckSandboxedGates(file, "near", refusal, true)

	require.Len(t, found, 2)
	assert.Equal(t, 2, found[0].Line)
	assert.Contains(t, found[0].Text, "gate")
	assert.Contains(t, found[0].Text, "project near")
	assert.Contains(t, found[0].Text, `sandbox = "off"`)
	assert.Equal(t, 3, found[1].Line)
}

func TestCheckSandboxedGates_RefusesAFileThatLoadsTheBatchLibraryForItsGates(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "load(\"@beehest//batch.star\", \"batch_merge\")\n\ndef main(item):\n    batch_merge(prs = [], base = \"main\", repo = \"o/r\", name = \"c\")\n"}, "workflow.star")

	found := workflow.CheckSandboxedGates(file, "far", refusal, false)

	require.NotEmpty(t, found, "the library runs a program gate here when the call passes gate_run")
	assert.Contains(t, found[0].File, "batch.star")
}

func TestCheckSandboxedGates_AcceptsAMergeOfAUnionWhichRunsNoGate(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    merge(members = [], base = \"main\", repo = \"o/r\", name = \"c\")\n"}, "workflow.star")

	assert.Empty(t, workflow.CheckSandboxedGates(file, "near", refusal, true))
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

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    gate(name = \"a\")\n    gate(run = [\"true\"], name = \"b\")\n    merge(members = [], base = \"main\", repo = \"o/r\", name = \"c\")\n"}, "workflow.star")

	found := workflow.CheckSandboxedGates(file, "far", refusal, false)

	require.Len(t, found, 1)
	assert.Equal(t, 3, found[0].Line)
}

func TestCheckSandboxedGates_IgnoresAFunctionNamedLikeABuiltin(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def gate(name):\n    return name\ndef main(item):\n    gate(name = \"a\")\n"}, "workflow.star")

	assert.Empty(t, workflow.CheckSandboxedGates(file, "near", refusal, true))
}
