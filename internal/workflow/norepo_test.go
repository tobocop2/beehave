package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

func TestCheckForNoRepository_RefusesEachGitBuiltinCallWithItsPosition(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    gate(name = \"check\")\n    preflight(name = \"look\")\n    merge(name = \"land\")\n    agent(prompt = \"p\", name = \"a\")\n"}, "workflow.star")

	found := workflow.CheckNoRepository(file, "desk")

	require.Len(t, found, 3)
	assert.Equal(t, 2, found[0].Line)
	assert.Contains(t, found[0].Text, "gate")
	assert.Contains(t, found[0].Text, "project desk has no repository")
	assert.Contains(t, found[1].Text, "preflight")
	assert.Contains(t, found[2].Text, "merge")
}

func TestCheckForNoRepository_AcceptsAGateThatNamesItsOwnProgramAndRefusesABareGate(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    gate(run = [\"true\"], name = \"links\")\n    gate(name = \"project\")\n"}, "workflow.star")

	found := workflow.CheckNoRepository(file, "desk")

	require.Len(t, found, 1)
	assert.Equal(t, 3, found[0].Line)
}

func TestCheckForNoRepository_RefusesAGateWhoseRunIsALiteralEmptyListOrNone(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "program = [\"true\"]\ndef main(item):\n    gate(run = [], name = \"a\")\n    gate(run = None, name = \"b\")\n    gate(run = program, name = \"c\")\n    gate(run = (), name = \"d\")\n    gate(run = ([]), name = \"e\")\n"}, "workflow.star")

	found := workflow.CheckNoRepository(file, "desk")

	require.Len(t, found, 4)
	assert.Equal(t, 3, found[0].Line)
	assert.Equal(t, 4, found[1].Line)
	assert.Equal(t, 6, found[2].Line)
	assert.Equal(t, 7, found[3].Line)
}

func TestCheckForNoRepository_AcceptsAWorkflowWithoutGitBuiltins(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def main(item):\n    command(run = [\"true\"])\n"}, "workflow.star")

	assert.Empty(t, workflow.CheckNoRepository(file, "desk"))
}

func TestCheckForNoRepository_IgnoresAFunctionNamedLikeABuiltin(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{"workflow.star": "def gate(name):\n    return name\ndef main(item):\n    gate(\"x\")\n"}, "workflow.star")

	assert.Empty(t, workflow.CheckNoRepository(file, "desk"))
}
