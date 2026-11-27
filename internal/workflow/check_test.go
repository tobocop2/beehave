package workflow_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/workflow"
)

func problems(found []workflow.Diagnostic) []string {
	out := make([]string, len(found))
	for i, d := range found {
		out[i] = fmt.Sprintf("%d:%d %s", d.Line, d.Col, d.Text)
	}

	return out
}

func TestCheck_AcceptsAFileWithMainAndLoads(t *testing.T) {
	t.Parallel()

	file := folder(t, map[string]string{
		"workflow.star": "load(\"@beehest//review_fix.star\", \"review_fix\")\ndef main(item):\n    return gate(name = \"check\")\n",
	}, "workflow.star")

	found, unparsed := workflow.Check(file)

	assert.Empty(t, found)
	assert.False(t, unparsed)
}

func TestCheck_ReportsEachProblemWithItsPlace(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		files    map[string]string
		want     string
		unparsed bool
	}{
		"a syntax error":        {map[string]string{"workflow.star": "def main(item:\n"}, "1:", true},
		"a name not defined":    {map[string]string{"workflow.star": "def main(item):\n    return nothing(1)\n"}, "2:12 undefined: nothing.", true},
		"a missing main":        {map[string]string{"workflow.star": "x = 1\n"}, "0:0 the file defines no function main", false},
		"a finish with no item": {map[string]string{"workflow.star": "def main(item):\n    pass\ndef finish():\n    pass\n"}, "0:0 finish takes the item. Write def finish(item):", false},
		"a load of a bad path":  {map[string]string{"workflow.star": "load(\"lib.star\", \"x\")\ndef main(item):\n    return x\n"}, "1:1 cannot load \"lib.star\"", false},
		"a load of no file":     {map[string]string{"workflow.star": "load(\"//lib.star\", \"x\")\ndef main(item):\n    return x\n"}, "1:1 cannot read lib.star", true},
		"an unknown library":    {map[string]string{"workflow.star": "load(\"@beehest//nothing.star\", \"x\")\ndef main(item):\n    return x\n"}, "1:1 the binary has no library", false},
		"two names not defined": {map[string]string{"workflow.star": "def main(item):\n    return nothing(1) + other(2)\n"}, "2:12 undefined: nothing.", true},
		"a problem in a load": {map[string]string{
			"workflow.star": "load(\"//lib.star\", \"x\")\ndef main(item):\n    return x\n",
			"lib.star":      "x = nothing\n",
		}, "1:5 undefined: nothing.", true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			found, unparsed := workflow.Check(folder(t, test.files, "workflow.star"))

			require.NotEmpty(t, found)
			assert.Contains(t, problems(found)[0], test.want)
			assert.Equal(t, test.unparsed, unparsed)
		})
	}
}

func TestCheck_AcceptsAFinishThatTakesTheItem(t *testing.T) {
	t.Parallel()

	found, _ := workflow.Check(folder(t, map[string]string{"workflow.star": "def main(item):\n    pass\ndef finish(item):\n    pass\n"}, "workflow.star"))

	assert.Empty(t, found)
}

func TestCheck_AKeywordThatTheAgentStepLostIsRefusedByName(t *testing.T) {
	t.Parallel()

	for _, removed := range []string{"kind", "lane", "rounds", "follow_ups", "require_skills"} {
		found, unparsed := workflow.Check(folder(t, map[string]string{
			"workflow.star": "def main(item):\n    return agent(model = \"m\", prompt = \"p\", " + removed + " = \"x\")\n",
		}, "workflow.star"))

		require.Len(t, found, 1, removed)
		assert.Contains(t, found[0].Text, "agent takes no argument "+removed, removed)
		assert.False(t, unparsed, removed)
	}
}

func TestCheck_ALiteralFollowUpsInACallThatIsNotAnAgentCallOrIsPositionalIsLeftAlone(t *testing.T) {
	t.Parallel()

	found, _ := workflow.Check(folder(t, map[string]string{
		"workflow.star": "def main(item):\n    struct(follow_ups = \"maybe\")\n    return agent(\"p\", model = \"m\")\n",
	}, "workflow.star"))

	assert.Empty(t, found)
}

func TestCheck_ReportsAFileThatDoesNotExist(t *testing.T) {
	t.Parallel()

	found, unparsed := workflow.Check(folder(t, nil, "workflow.star"))

	require.Len(t, found, 1)
	assert.Contains(t, found[0].Text, "cannot read workflow.star")
	assert.True(t, unparsed)
}

func TestCheck_ReportsFilesThatLoadEachOther(t *testing.T) {
	t.Parallel()

	found, _ := workflow.Check(folder(t, map[string]string{
		"workflow.star": "load(\"//a.star\", \"a\")\ndef main(item):\n    return a\n",
		"a.star":        "load(\"//workflow.star\", \"main\")\na = main\n",
	}, "workflow.star"))

	require.NotEmpty(t, found)
	assert.Contains(t, found[0].Text, "files load each other: workflow.star -> a.star -> workflow.star")
}
