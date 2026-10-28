package workflow_test

import (
	"testing"
	"testing/fstest"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"

	"github.com/tobocop2/obee/internal/workflow"
)

const scopeFile = `workflow "w" {}
agent "fix" {
  prompt  = "prompts/fix.md"
  outputs = ["verdict"]
}
agent "open" {
  prompt  = "prompts/fix.md"
  outputs = [item.id]
}
command "lint" {
  run = ["make", "lint"]
}
gate "check" {}
wait "ask" {
  question = "go?"
}
loop "rounds" {
  max   = 3
  until = true

  command "inner" {
    run = ["true"]
  }
}
`

func scopeSteps(t *testing.T) map[string]workflow.Step {
	t.Helper()

	flow, err := workflow.Load(fstest.MapFS{"w.hcl": {Data: []byte(scopeFile)}}, "w.hcl")
	require.NoError(t, err)

	steps := map[string]workflow.Step{}
	flow.Walk(func(step workflow.Step, _ int) { steps[step.Label()] = step })

	return steps
}

func expression(t *testing.T, text string) hcl.Expression {
	t.Helper()

	expr, diags := hclsyntax.ParseExpression([]byte(text), "w.hcl", hcl.Pos{Line: 3, Column: 7})
	require.False(t, diags.HasErrors())

	return expr
}

func newScope(t *testing.T) (*workflow.Scope, map[string]workflow.Step) {
	t.Helper()

	return workflow.NewScope(map[string]string{"id": "p-1", "title": "T"}, map[string]string{"MODE": "fix"}), scopeSteps(t)
}

func TestScopeReadsTheItemAndTheEnvironment(t *testing.T) {
	t.Parallel()

	scope, _ := newScope(t)

	got, err := scope.Value(expression(t, `item.id == "p-1" && env.MODE == "fix"`), 0)

	require.NoError(t, err)
	assert.Equal(t, cty.True, got)
}

func TestScopeReadsTheOutputsOfAStepThatRan(t *testing.T) {
	t.Parallel()

	scope, steps := newScope(t)
	scope.Record(steps["check"], map[string]cty.Value{"passed": cty.True})
	scope.Record(steps["lint"], map[string]cty.Value{"stdout": cty.StringVal("ok"), "rc": cty.NumberIntVal(0)})
	scope.Record(steps["fix"], map[string]cty.Value{"verdict": cty.StringVal("approve"), "text": cty.StringVal("done")})
	scope.Record(steps["ask"], map[string]cty.Value{"answer": cty.StringVal("go")})

	ok, err := scope.Bool(expression(t, `gate.check.passed && command.lint.rc == 0 && command.lint.stdout == "ok" && agent.fix.verdict == "approve" && wait.ask.answer == "go"`), 0)

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestScopeTypesTheOutputsOfAStep(t *testing.T) {
	t.Parallel()

	scope, steps := newScope(t)
	scope.Record(steps["check"], map[string]cty.Value{"passed": cty.True})

	got, err := scope.Value(expression(t, `gate.check`), 0)

	require.NoError(t, err)
	assert.Equal(t, cty.Bool, got.GetAttr("passed").Type())
	assert.Equal(t, cty.String, got.GetAttr("report").Type())
	assert.True(t, got.GetAttr("report").IsNull(), "an output the step did not record is a typed null")
}

func TestScopeKeepsTheOutputsOfAnAgentWhoseOutputListIsAnExpression(t *testing.T) {
	t.Parallel()

	scope, steps := newScope(t)
	scope.Record(steps["open"], map[string]cty.Value{"anything": cty.StringVal("x")})

	got, err := scope.Value(expression(t, `agent.open.anything`), 0)

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("x"), got)
}

func TestScopeGivesASkippedStepNullsOfTheRightType(t *testing.T) {
	t.Parallel()

	scope, steps := newScope(t)
	scope.Skip(steps["fix"])

	got, err := scope.Value(expression(t, `agent.fix.verdict`), 0)

	require.NoError(t, err)
	assert.True(t, got.IsNull())
	assert.Equal(t, cty.String, got.Type())
}

func TestScopeCountsLoopRoundsAndReportsExhaustion(t *testing.T) {
	t.Parallel()

	scope, steps := newScope(t)
	scope.Record(steps["rounds"], map[string]cty.Value{"exhausted": cty.True})

	ok, err := scope.Bool(expression(t, `loop.round == 2 && loop.rounds.exhausted`), 2)

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestScopeHasNoLoopRoundOutsideALoop(t *testing.T) {
	t.Parallel()

	scope, _ := newScope(t)

	_, err := scope.Value(expression(t, `loop.round`), 0)

	require.Error(t, err)
}

func TestScopeNamesAStepThatHasNotRunYet(t *testing.T) {
	t.Parallel()

	scope, _ := newScope(t)

	_, err := scope.Value(expression(t, `agent.fix.verdict`), 0)

	require.EqualError(t, err, "w.hcl:3:7: `agent.fix` has not run yet. Move the step up or remove the reference.")
}

func TestScopeTellsAnExpressionThatFailsWithoutParserWords(t *testing.T) {
	t.Parallel()

	scope, _ := newScope(t)

	_, err := scope.Value(expression(t, `item.id + 1`), 0)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "w.hcl:3:7: this expression cannot be evaluated: ")
	assert.Contains(t, err.Error(), " Check the names and the types it uses.")
	assert.NotContains(t, err.Error(), "Diagnostic")
}

func TestScopeRefusesAFunctionCallWithAPositionedError(t *testing.T) {
	t.Parallel()

	scope, _ := newScope(t)

	_, err := scope.Value(expression(t, `upper("x")`), 0)

	require.EqualError(t, err, "w.hcl:3:7: this expression cannot be evaluated: Functions may not be called here. Check the names and the types it uses.")
}

func TestScopeBoolRefusesANonBoolValue(t *testing.T) {
	t.Parallel()

	scope, _ := newScope(t)

	_, err := scope.Bool(expression(t, `item.id`), 0)

	require.EqualError(t, err, "w.hcl:3:7: this condition is a string, not true or false. Write a comparison.")
}

func TestScopeBoolRefusesANullValue(t *testing.T) {
	t.Parallel()

	scope, steps := newScope(t)
	scope.Skip(steps["check"])

	_, err := scope.Bool(expression(t, `gate.check.passed`), 0)

	require.EqualError(t, err, "w.hcl:3:7: this condition has no value, because a skipped step gave null. Compare the output with a value or guard it.")
}

func TestScopeBoolPassesTheErrorOfTheValue(t *testing.T) {
	t.Parallel()

	scope, _ := newScope(t)

	_, err := scope.Bool(expression(t, `agent.fix.verdict`), 0)

	require.ErrorContains(t, err, "has not run yet")
}

// A skipped step gives null. The language says: null == "" is false, null != "" is true, and a
// bare condition on null is an error. when, until and inputs read the same Value and Bool.
func TestScopeComparesNullWithEqualsAsFalseAndWithNotEqualsAsTrue(t *testing.T) {
	t.Parallel()

	scope, steps := newScope(t)
	scope.Skip(steps["fix"])

	for text, want := range map[string]bool{
		`agent.fix.verdict == ""`:   false,
		`agent.fix.verdict != ""`:   true,
		`agent.fix.verdict == null`: true,
		`agent.fix.verdict != null`: false,
	} {
		got, err := scope.Bool(expression(t, text), 0)
		require.NoError(t, err, text)
		assert.Equal(t, want, got, text)

		value, err := scope.Value(expression(t, text), 0)
		require.NoError(t, err, text)
		assert.Equal(t, cty.BoolVal(want), value, text)
	}

	_, err := scope.Bool(expression(t, `agent.fix.verdict`), 0)
	require.Error(t, err, "a bare condition on null is an error")
}

func TestOutputsDeclareOneListForEachKind(t *testing.T) {
	t.Parallel()

	names := func(kind workflow.Kind) []string {
		var got []string

		for _, o := range workflow.KindOutputs(kind) {
			got = append(got, o.Name)
		}

		return got
	}

	assert.Equal(t, []string{"head_sha", "session", "text", "transcript", "violations"}, names(workflow.KindAgent))
	assert.Equal(t, []string{"rc", "stdout"}, names(workflow.KindCommand))
	assert.Equal(t, []string{"head", "passed", "report"}, names(workflow.KindGate))
	assert.Equal(t, []string{"answer"}, names(workflow.KindWait))
	assert.Equal(t, []string{"exhausted"}, names(workflow.KindLoop))
	assert.Equal(t, []string{"reason", "state"}, names(workflow.KindMerge))
}
