package flow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// These tests run the gate list step of the example: the round derives its gate list from the gate files of the project and holds gate_steps to it by gate_map.

const (
	ciFile   = ".github/workflows/ci.yaml"
	lintFile = ".github/workflows/lint.yaml"
)

// gateStepsRig is a round rig whose project sets gate_steps in place of gate, and the gate files and the map in its vars.
func gateStepsRig(t *testing.T, held map[string]any) *roundRig {
	t.Helper()

	rig := newRoundRig(t, "")
	rig.project.Gate = ""
	rig.project.GateSteps = []model.GateSpec{{Name: "vet", Command: "make vet"}, {Name: "test", Command: "make test"}}
	rig.project.Vars["gate_files"] = []any{ciFile, lintFile}
	rig.project.Vars["gate_map"] = held

	return rig
}

func TestExampleProfile_TheRoundPassesTheGateStepsAndTheMapToTheDerive(t *testing.T) {
	t.Parallel()

	rig := gateStepsRig(t, map[string]any{"make vet": []any{"vet"}, "go install x@v1": []any{}})

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Equal(t, []string{`beehave check gate-list --steps vet --steps test --map {"go install x@v1":[],"make vet":["vet"]} ` + ciFile + " " + lintFile}, commandsNamed(rig, "beehave check gate-list"))
}

func TestExampleProfile_AMapThatDoesNotMatchTheCICommandsStopsTheRoundBeforeItsFirstPass(t *testing.T) {
	t.Parallel()

	rig := gateStepsRig(t, map[string]any{})

	previous := rig.h.commands.reply
	rig.h.commands.reply = func(request CommandRequest) (CommandOutput, error) {
		if len(request.Run) > 2 && request.Run[0] == "beehave" && request.Run[2] == "gate-list" {
			return CommandOutput{RC: 2, Stdout: "unmapped ship:test: make test"}, nil
		}

		return previous(request)
	}

	result, err := rig.run(t)

	require.NoError(t, err)
	require.NotNil(t, result.Stopped)
	assert.Equal(t, 2, result.Stopped.RC)
	assert.Contains(t, result.Stopped.Output, "unmapped ship:test")
	assert.Empty(t, commandsNamed(rig, syncProgram), "no pass ran after the refusal")
}

func TestExampleProfile_AProjectWithGateFilesAndNoGateStepsStopsTheRound(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")
	rig.project.Vars["gate_files"] = []any{ciFile}

	_, err := rig.run(t)

	require.ErrorContains(t, err, "REFUSE: vars.gate_files is set, but the project has no gate_steps")
	assert.Empty(t, commandsNamed(rig, "beehave check gate-list"), "the derive does not run")
	assert.Empty(t, commandsNamed(rig, syncProgram), "no pass ran after the refusal")
}

func TestExampleProfile_AGateMapOfTheWrongFormIsRefused(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]any{
		"a list":                   []any{"make test"},
		"a text":                   "make test",
		"a blank command":          map[string]any{" ": []any{"test"}},
		"a text as the step names": map[string]any{"make test": "test"},
		"a blank step name":        map[string]any{"make test": []any{""}},
		"a number as a step name":  map[string]any{"make test": []any{json.Number("3")}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := refusalOf(t, varsWith("gate_map", value))

			assert.Contains(t, got, "REFUSE: vars.gate_map ")
		})
	}

	_, err := settingsOf(t, varsWith("gate_map", map[string]any{"make test": []any{"test"}, "go install x@v1": []any{}}))
	require.NoError(t, err, "a dict of text to lists of text passes")
}

func TestExampleProfile_AProjectWithNoGateFilesDerivesNoGateList(t *testing.T) {
	t.Parallel()

	rig := newRoundRig(t, "")

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, commandsNamed(rig, "beehave check gate-list"))
}
