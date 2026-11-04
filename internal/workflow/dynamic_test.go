package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/workflow"
)

func TestDynamicCommands_CountsTheCallsWhoseRunListIsNotALiteral(t *testing.T) {
	t.Parallel()

	file := repository(t, map[string]string{
		"flow.star": "def main(item):\n    argv = [\"./s.sh\"]\n    command(run = argv)\n    command(run = [\"./\" + \"s.sh\"])\n    command(run = [\"/bin/sh\"] + [\"-c\"])\n" +
			"    command(run = [\"true\"])\n    command(run = argv, host = \"box\")\n",
		"s.sh": "echo\n",
	}, "flow.star")

	assert.Equal(t, 3, workflow.DynamicCommands(file), "a literal list and a call with a host are not counted")
}

func TestDynamicCommands_CountsAReadBackOrUndoOfAWriteThatIsNotALiteral(t *testing.T) {
	t.Parallel()

	file := repository(t, map[string]string{
		"flow.star": "def main(item):\n    argv = [\"./s.sh\"]\n    write(run = [\"make\"], read_back = argv, undo = argv + [\"x\"])\n    write(run = [\"make\"], read_back = [\"true\"], undo = [\"true\"])\n    write(run = [\"make\"])\n",
	}, "flow.star")

	assert.Equal(t, 2, workflow.DynamicCommands(file), "each dynamic read_back or undo counts once; a call without them counts nothing")
}
