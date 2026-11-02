package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/workflow"
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
