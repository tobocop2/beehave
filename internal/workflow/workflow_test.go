package workflow_test

import (
	"errors"
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/hclfile"
	"github.com/tobocop2/obee/internal/workflow"
)

const lintAndFix = `workflow "lint-and-fix" {
  method_version = "lint-1"
}

loop "tidy" {
  max   = 3
  until = command.lint.rc == 0

  command "lint" {
    run = ["golangci-lint", "run"]
  }

  agent "fix" {
    prompt = "prompts/fix.md"
    model  = loop.round <= 2 ? "sonnet" : "opus"
    when   = command.lint.rc != 0
  }
}
`

func load(t *testing.T, text string) (*workflow.Workflow, error) {
	t.Helper()

	return workflow.Load(fstest.MapFS{"w.hcl": {Data: []byte(text)}}, "w.hcl")
}

func problems(t *testing.T, err error) []hclfile.Diagnostic {
	t.Helper()

	require.Error(t, err)

	var found []hclfile.Diagnostic

	var many interface{ Unwrap() []error }

	if errors.As(err, &many) {
		for _, e := range many.Unwrap() {
			var d hclfile.Diagnostic

			require.ErrorAs(t, e, &d)

			found = append(found, d)
		}

		return found
	}

	var d hclfile.Diagnostic

	require.ErrorAs(t, err, &d)

	return append(found, d)
}

func only(t *testing.T, text string) hclfile.Diagnostic {
	t.Helper()

	_, err := load(t, text)
	found := problems(t, err)
	require.Len(t, found, 1, "%v", found)

	return found[0]
}

type seen struct {
	kind  workflow.Kind
	name  string
	depth int
}

func order(w *workflow.Workflow) []seen {
	var got []seen

	w.Walk(func(step workflow.Step, depth int) {
		got = append(got, seen{kind: step.Kind(), name: step.Label(), depth: depth})
	})

	return got
}

func TestLoadWalksAnUnrelatedLintAndFixLoopInOrder(t *testing.T) {
	t.Parallel()

	w, err := load(t, lintAndFix)

	require.NoError(t, err)
	assert.Equal(t, "w.hcl", w.File)
	assert.Equal(t, "lint-and-fix", w.Name)
	assert.Equal(t, "lint-1", w.MethodVersion)
	assert.False(t, w.RequireModel)
	assert.Equal(t, []seen{
		{kind: workflow.KindLoop, name: "tidy", depth: 0},
		{kind: workflow.KindCommand, name: "lint", depth: 1},
		{kind: workflow.KindAgent, name: "fix", depth: 1},
	}, order(w))
}

func TestLoadKeepsTopLevelStepsInFileOrderAroundALoop(t *testing.T) {
	t.Parallel()

	w, err := load(t, `
workflow "all" {
  require_model = true
}
agent "first" {
  prompt = "p.md"
  model  = "opus"
}
loop "mid" {
  max   = 2
  until = gate.g.passed
  gate "g" {
    run = ["make", "check"]
  }
}
wait "last" {
  question = "go on?"
}
command "tail" {
  run = ["true"]
}
`)

	require.NoError(t, err)
	assert.True(t, w.RequireModel)
	assert.Equal(t, []seen{
		{kind: workflow.KindAgent, name: "first", depth: 0},
		{kind: workflow.KindLoop, name: "mid", depth: 0},
		{kind: workflow.KindGate, name: "g", depth: 1},
		{kind: workflow.KindWait, name: "last", depth: 0},
		{kind: workflow.KindCommand, name: "tail", depth: 0},
	}, order(w))
}

func TestLoadKeepsEachAttributeAsAnExpression(t *testing.T) {
	t.Parallel()

	w, err := load(t, lintAndFix)
	require.NoError(t, err)

	loop, ok := w.Steps[0].(*workflow.Loop)
	require.True(t, ok)
	assert.Equal(t, 3, loop.Max)
	assert.NotEmpty(t, loop.Until.Variables())

	agent, ok := loop.Steps[1].(*workflow.Agent)
	require.True(t, ok)
	assert.NotEmpty(t, agent.Model.Variables())
	assert.NotEmpty(t, agent.When.Variables())
}

func TestStepPositionPointsAtTheBlockHeader(t *testing.T) {
	t.Parallel()

	w, err := load(t, lintAndFix)
	require.NoError(t, err)

	var lines []string

	w.Walk(func(step workflow.Step, _ int) {
		lines = append(lines, fmt.Sprintf("%s:%d", step.Label(), step.Pos().Start.Line))
	})

	assert.Equal(t, []string{"tidy:5", "lint:9", "fix:13"}, lines)
}

func TestLoadReportsAnUnreadableFile(t *testing.T) {
	t.Parallel()

	_, err := workflow.Load(fstest.MapFS{}, "none.hcl")

	found := problems(t, err)
	assert.Equal(t, "none.hcl", found[0].File)
	assert.Contains(t, found[0].Text, "cannot read the file")
}

func TestLoadReportsASyntaxError(t *testing.T) {
	t.Parallel()

	_, err := load(t, "workflow \"a\" {\n  x = \n}\n")

	found := problems(t, err)
	assert.Equal(t, 2, found[0].Line)
}

func TestUnknownTopLevelBlockNamesTheValidBlocks(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"a\" {}\nmerge \"m\" {}\n")

	assert.Equal(t, 2, d.Line)
	assert.Equal(t, 1, d.Col)
	assert.Contains(t, d.Text, "`merge`")
	assert.Contains(t, d.Text, "workflow, agent, command, gate, loop, wait")
}

func TestUnknownBlockInALoopExcludesTheWorkflowBlock(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"a\" {}\nloop \"l\" {\n  max = 1\n  until = true\n  workflow \"b\" {}\n}\n")

	assert.Equal(t, 5, d.Line)
	assert.Contains(t, d.Text, "agent, command, gate, loop, wait")
	assert.NotContains(t, d.Text, "workflow,")
}

func TestUnknownArgumentAtTheTopOfTheFile(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"a\" {}\nstray = 1\n")

	assert.Equal(t, 2, d.Line)
	assert.Contains(t, d.Text, "`stray`")
}

func TestUnknownArgumentInAStepIsPositioned(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"a\" {}\ncommand \"c\" {\n  run = [\"x\"]\n  bogus = 1\n}\n")

	assert.Equal(t, 4, d.Line)
	assert.Contains(t, d.Text, "bogus")
}

func TestUnknownArgumentInALoopIsPositioned(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"a\" {}\nloop \"l\" {\n  max = 1\n  until = true\n  bogus = 1\n}\n")

	assert.Equal(t, 5, d.Line)
	assert.Contains(t, d.Text, "bogus")
}

func TestUnknownArgumentInTheWorkflowBlock(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"a\" {\n  tracker = \"x\"\n}\n")

	assert.Equal(t, 2, d.Line)
	assert.Contains(t, d.Text, "tracker")
}

func TestMissingRequiredAttributesName(t *testing.T) {
	t.Parallel()

	cases := map[string]struct{ body, key string }{
		"agent prompt":   {"agent \"a\" {\n}\n", "prompt"},
		"command run":    {"command \"c\" {\n}\n", "run"},
		"loop max":       {"loop \"l\" {\n  until = true\n}\n", "max"},
		"loop until":     {"loop \"l\" {\n  max = 1\n}\n", "until"},
		"a loop is deep": {"loop \"l\" {\n  max = 1\n  until = true\n  agent \"a\" {\n  }\n}\n", "prompt"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := only(t, "workflow \"w\" {}\n"+tc.body)

			assert.Contains(t, d.Text, "`"+tc.key+"` is required")
			assert.Positive(t, d.Line)
		})
	}
}

func TestEveryMissingAttributeIsReportedInPositionOrder(t *testing.T) {
	t.Parallel()

	_, err := load(t, "workflow \"w\" {}\ncommand \"c\" {\n}\nagent \"a\" {\n}\n")

	found := problems(t, err)
	require.Len(t, found, 2)
	assert.Less(t, found[0].Line, found[1].Line)
}

func TestRepeatedStepNameIsReportedAtTheSecondUse(t *testing.T) {
	t.Parallel()

	d := only(t, `
workflow "w" {}
command "x" {
  run = ["a"]
}
loop "l" {
  max   = 1
  until = true
  gate "x" {
  }
}
`)

	assert.Equal(t, 9, d.Line)
	assert.Contains(t, d.Text, "`x`")
	assert.Contains(t, d.Text, "line 3")
}

func TestMaxBelowOneIsReportedAtTheValue(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"w\" {}\nloop \"l\" {\n  max   = 0\n  until = true\n}\n")

	assert.Equal(t, 3, d.Line)
	assert.Equal(t, 11, d.Col)
	assert.Contains(t, d.Text, "`max` must be at least 1")
}

func TestMaxAboveZeroPasses(t *testing.T) {
	t.Parallel()

	_, err := load(t, "workflow \"w\" {}\nloop \"l\" {\n  max   = 1\n  until = true\n}\n")

	require.NoError(t, err)
}

func TestAnAgentWithoutAModelFailsWhenRequireModelIsSet(t *testing.T) {
	t.Parallel()

	d := only(t, `
agent "late" {
  prompt = "p.md"
}
workflow "w" {
  require_model = true
}
agent "ok" {
  prompt = "p.md"
  model  = loop.round > 1 ? "a" : "b"
}
`)

	assert.Equal(t, 2, d.Line)
	assert.Contains(t, d.Text, "`late`")
	assert.Contains(t, d.Text, "`model`")
}

func TestAnAgentWithANullModelFailsWhenRequireModelIsSet(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"w\" {\n  require_model = true\n}\nagent \"a\" {\n  prompt = \"p\"\n  model = null\n}\n")

	assert.Equal(t, 4, d.Line)
}

func TestAnAgentWithoutAModelPassesWhenRequireModelIsOff(t *testing.T) {
	t.Parallel()

	_, err := load(t, "workflow \"w\" {\n  require_model = false\n}\nagent \"a\" {\n  prompt = \"p\"\n}\n")

	require.NoError(t, err)
}

func TestRequireModelChecksAgentsInsideALoop(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"w\" {\n  require_model = true\n}\nloop \"l\" {\n  max = 1\n  until = true\n  agent \"a\" {\n    prompt = \"p\"\n  }\n}\n")

	assert.Equal(t, 7, d.Line)
}

func TestTheWorkflowBlockIsRequiredOnce(t *testing.T) {
	t.Parallel()

	d := only(t, "command \"c\" {\n  run = [\"x\"]\n}\n")
	assert.Equal(t, "w.hcl", d.File)
	assert.Equal(t, 0, d.Line)
	assert.Contains(t, d.Text, "`workflow` block")

	d = only(t, "workflow \"a\" {}\nworkflow \"b\" {}\n")
	assert.Equal(t, 2, d.Line)
	assert.Contains(t, d.Text, "once")
}

func TestABlockNeedsExactlyOneName(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"w\" {}\nagent {\n  prompt = \"p\"\n}\n")
	assert.Equal(t, 2, d.Line)
	assert.Contains(t, d.Text, "`agent` needs one name")

	d = only(t, "workflow {\n}\n")
	assert.Equal(t, 1, d.Line)
	assert.Contains(t, d.Text, "`workflow` needs one name")
}

func TestAWrongTypeIsReportedWithItsKey(t *testing.T) {
	t.Parallel()

	d := only(t, "workflow \"w\" {\n  require_model = \"yes\"\n}\n")

	assert.Equal(t, 2, d.Line)
	assert.Contains(t, d.Text, "`require_model` has the wrong type")
}
