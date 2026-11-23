package flow

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dryLocalFile is the workflow of the local example as it is.
var dryLocalFile = filepath.Join("..", "..", "examples", "local", "workflow.star")

func TestDryRun_ALocalExampleGateWithNoVerdictRunsAgainAndStartsNoFixAgent(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryLocalFile, "12", nil, &out, WithVoidGates(1)), out.String())

	text := out.String()
	assert.Equal(t, 2, strings.Count(text, " gate:check#"), "the void gate ran again: %s", text)
	assert.NotContains(t, text, "agent:fix", "a gate with no verdict starts no fix agent")
	assert.Contains(t, text, "agent:review", "the gate that ran again passed and went to the review")
}

func TestDryRun_ALocalExampleItemStopsWithAReasonAfterTheThirdGateWithNoVerdict(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	err := DryRun(t.Context(), dryLocalFile, "12", nil, &out, WithVoidGates(3))

	require.ErrorContains(t, err, "the gate gave no verdict 3 times in a row")
	assert.Equal(t, 3, strings.Count(out.String(), " gate:check#"), "the gate ran three times and no more: %s", out.String())
	assert.NotContains(t, out.String(), "agent:fix")
	assert.NotContains(t, out.String(), "agent:review", "the code was never judged, so nothing reviews it")
}

func TestDryRun_ALocalExampleRedGateWithEveryStepKnownStillGoesToTheFixAgent(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	require.NoError(t, DryRun(t.Context(), dryLocalFile, "12", nil, &out, WithRedGate()), out.String())

	assert.Contains(t, out.String(), "agent:fix", "a red gate is judged, so the fix agent runs")
	assert.Equal(t, 2, strings.Count(out.String(), " gate:check#"), "the gate ran once more after the fix: %s", out.String())
}

func TestDryRun_ALocalExampleGateWithAFailedStepAndAnUnknownStepStillGoesToTheFixAgent(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	rig.reply("", costing(1))
	rig.h.gate.results = []GateResult{
		{Passed: false, Steps: []GateStep{{Name: "command", Status: "fail"}, {Name: "tests", Status: "unknown"}}},
		{Passed: true},
	}

	_, err := rig.h.engine(t).RunFile(testItem, testProject, localExampleFile(t, false), modeOff())
	require.Error(t, err, "the fake review agent writes no verdict, so the run ends after the fix")

	names, err := rig.h.client(t, testVersion).stepNames(testRun)
	require.NoError(t, err)

	assert.Equal(t, 1, stepCount(names, "agent:fix#3/start"), "a gate with a failed step is red, so the fix agent ran once: %v", names)
	assert.Equal(t, 1, stepCount(names, "gate:check#1"), "the red gate did not run again before the fix: %v", names)
}

func TestDryRun_AVoidGateOptionMakesOnlyTheFirstGatesVoid(t *testing.T) {
	t.Parallel()

	const text = "def main(item):\n    a = gate(name = \"check\")\n    b = gate(name = \"check\")\n    fail(\"%s %s\" % (a.steps[0][\"status\"], b.steps[0][\"status\"]))\n"

	var out bytes.Buffer

	err := DryRun(t.Context(), dryFile(t, text), "12", nil, &out, WithVoidGates(1))

	require.ErrorContains(t, err, "unknown pass")
}
