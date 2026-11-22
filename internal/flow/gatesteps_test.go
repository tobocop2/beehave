package flow

import (
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

var (
	stepsPass    = []GateStep{{Name: "command", Status: "pass"}, {Name: "tests", Status: "pass"}}
	stepsUnknown = []GateStep{{Name: "command", Status: "pass"}, {Name: "tests", Status: "unknown"}}
)

func TestRecordGate_AStepThatCannotBeRecordedFailsTheCall(t *testing.T) {
	t.Parallel()

	engine, err := NewEngine(t.Context(), newHarness(t).opts, Deps{})
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	s := &script{e: engine, ctx: engine.ctx, counts: map[string]int{}}

	_, err = s.recordGate("g", gateOutputs(true, "h", "", "", nil, nil, nil))

	require.Error(t, err)
}

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

func TestGateStep_TheRecordOfAGateHoldsTheNamesOfTheListThatItWasGivenTo(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	editedFacts(h, []model.GateSpec{{Name: "fmt", Command: "make fmt"}, {Name: "lint", Command: "make lint"}}, nil)
	h.gate.results = []GateResult{{Passed: true, Head: gatedHead, Steps: []GateStep{{Name: "fmt", Status: "pass"}}}}

	_, err := walkText(t, h, bodyFlow("c = gate(name = \"check\")\ncommand(run = [\"show\"] + c.required)\n"))

	require.NoError(t, err)
	assert.Equal(t, []string{"show fmt lint"}, h.commands.ran())
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

func TestFacts_TheGateProgramOfTheProjectIsInTheFactsAndAProjectWithNoneShowsAnEmptyList(t *testing.T) {
	t.Parallel()

	listed := factsOutputs(model.Project{GateProgram: []string{"Makefile", "scripts/"}})
	none := factsOutputs(model.Project{})

	assert.Equal(t, []string{"Makefile", "scripts/"}, listed["gate_program"])
	assert.Equal(t, []string{}, none["gate_program"])
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

func TestGateStep_AGateProgramRecordsPassedFromItsExitCode(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		rc   int
		want string
	}{"a zero code": {0, "show True"}, "a code that is not zero": {1, "show False"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := mergeHarness(t, merge.Result{Stop: merge.Done})
			h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
				if request.Run[0] == "check" {
					return CommandOutput{RC: test.rc}, nil
				}

				return CommandOutput{}, nil
			}

			_, err := walkText(t, h, bodyFlow("c = gate(run = [\"check\"], name = \"check\")\ncommand(run = [\"show\", str(c.passed)])\n"))

			require.NoError(t, err)
			assert.Equal(t, []string{"check", test.want}, h.commands.ran())
		})
	}
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
