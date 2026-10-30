package workflow_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
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
		"workflow.star": "load(\"@obee//review_fix.star\", \"review_fix\")\ndef main(item):\n    return gate(name = \"check\")\n",
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
		"an unknown library":    {map[string]string{"workflow.star": "load(\"@obee//nothing.star\", \"x\")\ndef main(item):\n    return x\n"}, "1:1 the binary has no library", false},
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

func TestCheck_RefusesABadFollowUpsValueAtItsPlaceAndAcceptsTheListedOnes(t *testing.T) {
	t.Parallel()

	for _, value := range []string{`"allow"`, `"forbid"`, `mode`} {
		found, _ := workflow.Check(folder(t, map[string]string{
			"workflow.star": "mode = \"allow\"\ndef main(item):\n    return agent(model = \"m\", prompt = \"p\", outputs = [\"verdict\"], follow_ups = " + value + ")\n",
		}, "workflow.star"))

		assert.Empty(t, found, value)
	}

	for _, value := range []string{`"maybe"`, `3`} {
		found, unparsed := workflow.Check(folder(t, map[string]string{
			"workflow.star": "def main(item):\n    return agent(model = \"m\", prompt = \"p\", follow_ups = " + value + ")\n",
		}, "workflow.star"))

		require.Len(t, found, 1, value)
		assert.Equal(t, 2, found[0].Line, value)
		assert.Equal(t, 58, found[0].Col, value)
		assert.Contains(t, found[0].Text, "follow_ups is "+value+". Use one of: allow, forbid.")
		assert.False(t, unparsed)
	}
}

func TestCheck_ALiteralForbidWhoseLiteralOutputsLackTheVerdictIsRefusedAtTheValue(t *testing.T) {
	t.Parallel()

	const head = "names = [\"verdict\"]\ndef main(item):\n    return agent(model = \"m\", prompt = \"p\", "

	for _, refused := range []string{`outputs = ["text"], follow_ups = "forbid")`, `outputs = [], follow_ups = "forbid")`, `follow_ups = "forbid")`} {
		found, _ := workflow.Check(folder(t, map[string]string{"workflow.star": head + refused + "\n"}, "workflow.star"))

		require.Len(t, found, 1, refused)
		assert.Equal(t, 3, found[0].Line, refused)
		assert.Equal(t, strings.Index(head[strings.LastIndex(head, "\n")+1:]+refused, `"forbid"`)+1, found[0].Col, refused)
		assert.Contains(t, found[0].Text, `follow_ups is "forbid", so outputs must list "verdict"`, refused)
	}

	for _, accepted := range []string{
		`outputs = ["text", "verdict"], follow_ups = "forbid")`,
		`outputs = names, follow_ups = "forbid")`,
		`outputs = ["text"] + names, follow_ups = "forbid")`,
		`outputs = ["text", names[0]], follow_ups = "forbid")`,
		`outputs = ["text"], follow_ups = "allow")`,
	} {
		found, _ := workflow.Check(folder(t, map[string]string{"workflow.star": head + accepted + "\n"}, "workflow.star"))

		assert.Empty(t, found, accepted)
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
