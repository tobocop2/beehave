package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
)

var (
	stepsPass    = []GateStep{{Name: "command", Status: "pass"}, {Name: "tests", Status: "pass"}}
	stepsUnknown = []GateStep{{Name: "command", Status: "pass"}, {Name: "tests", Status: "unknown"}}
)

func TestGateStep_TheResultOfEachStepIsAnOutputOfTheStep(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: false, Head: gatedHead, Steps: stepsUnknown}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nfor s in c.steps:\n    command(run = [\"show\", s[\"name\"], s[\"status\"]])\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{"show command pass", "show tests unknown"}, h.commands.ran())
}

func TestGateStep_AGateProgramGivesOneStepFromItsExitCode(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, bodyFlow("c = gate(run = [\"true\"], name = \"check\")\ncommand(run = [\"show\", c.steps[0][\"name\"], c.steps[0][\"status\"]])\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{"true", "show command pass"}, h.commands.ran())
}

func TestMergeStep_ACallMergesOnlyWhenEveryStepOfTheGateIsAPass(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Steps: stepsPass}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nmerge(head = c.head, steps = c.steps, merge = True, name = \"ship\")\n"))

	require.NoError(t, err)
	assert.Len(t, h.merge.given, 1)
}

func TestMergeStep_AStepThatIsUnknownRefusesTheMergeBeforeAnythingIsMarkedOrMerged(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: false, Head: gatedHead, Steps: stepsUnknown}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nmerge(head = c.head, steps = c.steps, merge = True, name = \"ship\")\n"))

	require.ErrorContains(t, err, `the gate step "tests" is unknown`)
	assert.Empty(t, h.merge.given)
	assert.Empty(t, h.merge.asked)
}

func TestMergeStep_AGateWithNoStepsIsUnknownAndRefusesTheMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Steps: []GateStep{}}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nmerge(head = c.head, steps = c.steps, merge = True, name = \"ship\")\n"))

	require.ErrorContains(t, err, "`steps` is empty")
	assert.Empty(t, h.merge.given)
}

func TestMergeStep_AStepsListThatIsNotTheStepsOfAGateIsRefused(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, shipFlow(`steps = [1], merge = True, `))

	require.ErrorContains(t, err, "`steps` must be the steps of a gate")
	assert.Empty(t, h.merge.given)
}

func TestGateStep_AGateProgramThatExitsWithAFailureGivesAStepThatFails(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if request.Run[0] == "false" {
			return CommandOutput{RC: 1}, nil
		}

		return CommandOutput{}, nil
	}

	_, err := walkText(t, h, bodyFlow("c = gate(run = [\"false\"], name = \"check\")\ncommand(run = [\"show\", c.steps[0][\"status\"]])\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{"false", "show fail"}, h.commands.ran())
}

func TestMergeStep_AStepsListThatHoldsAValueThatCannotBeReadIsRefused(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, shipFlow(`steps = [len], merge = True, `))

	require.ErrorContains(t, err, "`steps` must be the steps of a gate")
	assert.Empty(t, h.merge.given)
}

func TestMergeStep_AnUnknownStepIsNotReadWhenTheCallDoesNotMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: false, Head: gatedHead, Steps: stepsUnknown}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nmerge(head = c.head, steps = c.steps, name = \"ship\")\n"))

	require.NoError(t, err)
}
