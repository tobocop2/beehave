package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

func TestCheck_AcceptsTheJSONModuleAndStillRefusesAnUnknownName(t *testing.T) {
	t.Parallel()

	good := folder(t, map[string]string{
		"workflow.star": "def main(item):\n    return json.decode(json.indent(json.encode({\"a\": 1})))\n",
	}, "workflow.star")

	found, unparsed := workflow.Check(good)

	assert.Empty(t, found)
	assert.False(t, unparsed)

	bad := folder(t, map[string]string{
		"workflow.star": "def main(item):\n    return yaml.decode(\"a\")\n",
	}, "workflow.star")

	found, unparsed = workflow.Check(bad)

	require.NotEmpty(t, found)
	assert.Contains(t, problems(found)[0], "undefined: yaml.")
	assert.True(t, unparsed)
}
