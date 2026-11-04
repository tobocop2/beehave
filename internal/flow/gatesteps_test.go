package flow

import (
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/model"
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

func TestMergeStep_AStepsValueThatLacksARequiredStepRefusesTheMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.facts = func(string) model.Project {
		return model.Project{GateSteps: []model.GateSpec{{Name: "fmt", Command: "make fmt"}, {Name: "lint", Command: "make lint"}}}
	}
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Steps: []GateStep{{Name: "fmt", Status: "pass"}}}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nmerge(head = c.head, steps = c.steps, merge = True, name = \"ship\")\n"))

	require.ErrorContains(t, err, `has the step "lint", and `+"`steps`"+` has no result for it`)
	assert.Empty(t, h.merge.given)
}

func TestMergeStep_AStepsValueThatHoldsEveryRequiredStepMerges(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.facts = func(string) model.Project {
		return model.Project{GateSteps: []model.GateSpec{{Name: "fmt", Command: "make fmt"}}}
	}
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Steps: []GateStep{{Name: "fmt", Status: "pass"}}}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nmerge(head = c.head, steps = c.steps, merge = True, name = \"ship\")\n"))

	require.NoError(t, err)
	assert.Len(t, h.merge.given, 1)
}

// editedFacts gives the gate list of the project as it was before an edit, and the list after the edit once the program `edit` ran.
func editedFacts(h *harness, before, after []model.GateSpec) {
	var edited atomic.Bool

	h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if slices.Equal(request.Run, []string{"edit"}) {
			edited.Store(true)
		}

		return CommandOutput{}, nil
	}
	h.facts = func(string) model.Project {
		if edited.Load() {
			return model.Project{GateSteps: after}
		}

		return model.Project{GateSteps: before}
	}
}

const gateEditMerge = "c = gate(name = \"check\")\ncommand(run = [\"edit\"])\nmerge(head = c.head, steps = c.steps, merge = True, name = \"ship\")\n"

func TestMergeStep_AConfigEditBetweenTheGateAndTheMergeCannotRelaxTheRecordedSet(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	editedFacts(h, []model.GateSpec{{Name: "fmt", Command: "make fmt"}, {Name: "lint", Command: "make lint"}}, []model.GateSpec{{Name: "fmt", Command: "make fmt"}})
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Steps: []GateStep{{Name: "fmt", Status: "pass"}}}}

	_, err := walkText(t, h, bodyFlow(gateEditMerge))

	require.ErrorContains(t, err, `has the step "lint"`)
	assert.Empty(t, h.merge.given)
}

func TestMergeStep_AConfigEditBetweenTheGateAndTheMergeCannotBreakAMergeOfAGateThatRanTheWholeList(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	editedFacts(h, []model.GateSpec{{Name: "fmt", Command: "make fmt"}}, []model.GateSpec{{Name: "fmt", Command: "make fmt"}, {Name: "lint", Command: "make lint"}})
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Steps: []GateStep{{Name: "fmt", Status: "pass"}}}}

	_, err := walkText(t, h, bodyFlow(gateEditMerge))

	require.NoError(t, err)
	assert.Len(t, h.merge.given, 1)
}

func TestGateStep_TheRecordOfAGateHoldsTheNamesOfTheListThatItWasGivenTo(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	editedFacts(h, []model.GateSpec{{Name: "fmt", Command: "make fmt"}, {Name: "lint", Command: "make lint"}}, nil)
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Steps: []GateStep{{Name: "fmt", Status: "pass"}}}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\ncommand(run = [\"show\"] + c.required)\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{"show fmt lint"}, h.commands.ran())
}

func TestRecordedNames_AGateRecordWithoutTheFieldAsksForNoStepAndAListGivesItsNames(t *testing.T) {
	t.Parallel()

	assert.Nil(t, recordedNames(nil))
	assert.Nil(t, recordedNames(starlark.String("lint")))
	assert.Equal(t, []string{"fmt", "lint"}, recordedNames(starlark.NewList([]starlark.Value{starlark.String("fmt"), starlark.MakeInt(1), starlark.String("lint")})))
}

func TestFacts_ALineOfAGateListShowsTheNamesOfTheListAndAGateStringShowsNone(t *testing.T) {
	t.Parallel()

	list := factsOutputs(model.Project{Gate: "", GateSteps: []model.GateSpec{{Name: "fmt", Command: "make fmt"}, {Name: "lint", Command: "make lint"}}})
	plain := factsOutputs(model.Project{Gate: "make check"})

	assert.Equal(t, []string{"fmt", "lint"}, list["gate_steps"])
	assert.Equal(t, []string{}, plain["gate_steps"])
	assert.Equal(t, "fmt: make fmt; lint: make lint", list["gate"], "the gate that the prompts print is the list with its commands")
	assert.Equal(t, "make check", plain["gate"])
}

func TestMergeStep_AGateWithNoStepsIsUnknownAndRefusesTheMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Steps: []GateStep{}}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nmerge(head = c.head, steps = c.steps, merge = True, name = \"ship\")\n"))

	require.ErrorContains(t, err, "`steps` is empty")
	assert.Empty(t, h.merge.given)
}

func TestMergeStep_ACallThatMergesAndNamesNoStepsIsRefusedAndNamesTheFix(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	// A call with **kwargs is not read by the workflow check, so this reaches the call itself.
	_, err := walkText(t, h, bodyFlow("merge(**{\"head\": \""+gatedHead+"\", \"merge\": True, \"name\": \"ship\"})\n"))

	require.ErrorContains(t, err, "Add `steps = <gate>.steps`")
	assert.Empty(t, h.merge.given)
	assert.Empty(t, h.merge.asked)
}

func TestMergeStep_TheWorkflowCheckRefusesAMergeCallThatMergesAndNamesNoStepsBeforeTheRun(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, bodyFlow("merge(head = \""+gatedHead+"\", merge = True, name = \"ship\")\n"))

	require.ErrorContains(t, err, "merge needs the gate steps when it merges")
	assert.Empty(t, h.merge.given)
}

func TestMergeStep_ACallThatMergesAndPassesAnEmptyStepsListIsRefusedAndNamesTheFix(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, bodyFlow(shipCall(`steps = [] + [], merge = True, `)))

	require.ErrorContains(t, err, "`steps` is empty")
	require.ErrorContains(t, err, "Pass `steps = <gate>.steps`")
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

func TestMergeStep_AStepsListMadeByHandIsRefusedWhenNoGateOfTheRunGaveIt(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, bodyFlow("merge(head = \""+gatedHead+"\", steps = [{\"name\": \"command\", \"status\": \"pass\"}], merge = True, name = \"ship\")\n"))

	require.ErrorContains(t, err, "not the steps of a gate call in this run")
	require.ErrorContains(t, err, "Pass `steps = <gate>.steps`")
	require.ErrorContains(t, err, "a child run cannot merge with steps that its parent gated, so call the gate in the run that merges")
	assert.Empty(t, h.merge.given)
	assert.Empty(t, h.merge.asked)
}

func TestMergeStep_StepsOfAGateAtAnotherHeadAreRefusedAtTheHeadOfTheMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})

	_, err := walkText(t, h, bodyFlow("g = gate(run = [\"true\"], head = \"aaaa\", name = \"check\")\nmerge(head = \""+gatedHead+"\", steps = g.steps, merge = True, name = \"ship\")\n"))

	require.ErrorContains(t, err, "at the head of this merge")
	assert.Empty(t, h.merge.given)
	assert.Empty(t, h.merge.asked)
}

func TestMergeStep_AStepsListEditedAfterTheGateIsRefused(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: false, Head: gatedHead, Steps: stepsUnknown}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nmerge(head = c.head, steps = [{\"name\": \"command\", \"status\": \"pass\"}, {\"name\": \"tests\", \"status\": \"pass\"}], merge = True, name = \"ship\")\n"))

	require.ErrorContains(t, err, "not the steps of a gate call in this run")
	assert.Empty(t, h.merge.given)
}

func TestMergeStep_AnUnknownStepIsNotReadWhenTheCallDoesNotMerge(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.gate.results = []GateResult{{Passed: false, Head: gatedHead, Steps: stepsUnknown}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\nmerge(head = c.head, steps = c.steps, name = \"ship\")\n"))

	require.NoError(t, err)
}
