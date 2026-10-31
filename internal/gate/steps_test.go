package gate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
)

var (
	passed = []gate.Step{{Name: gate.StepCommand, Status: gate.StatusPass}}
	failed = []gate.Step{{Name: gate.StepCommand, Status: gate.StatusFail}}
)

func step(name string, status gate.Status) gate.Step { return gate.Step{Name: name, Status: status} }

func TestSteps_AGateWithNoTestReportGivesOneStepFromItsExitCode(t *testing.T) {
	t.Parallel()

	for code, want := range map[int]gate.Status{0: gate.StatusPass, 1: gate.StatusFail} {
		result, err := runWithReports(t, code, "", nil)

		require.NoError(t, err)
		assert.Equal(t, []gate.Step{step(gate.StepCommand, want)}, result.Steps)
	}
}

func TestSteps_AReportWithTestsGivesAPassForTheCommandAndForTheTests(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 0, "r/*.xml", map[string]string{"r/a.xml": rootSuite})

	require.NoError(t, err)
	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusPass), step(gate.StepTests, gate.StatusPass)}, result.Steps)
	assert.True(t, result.Passed)
}

func TestSteps_AGateThatExitsZeroWithNoResultLineIsUnknownAndNeverAPass(t *testing.T) {
	t.Parallel()

	for name, files := range map[string]map[string]string{
		"no report file":               nil,
		"a report with zero":           {"r/a.xml": zeroTests},
		"a report that does not parse": {"r/a.xml": badXML},
	} {
		result, err := runWithReports(t, 0, "r/*.xml", files)

		require.NoError(t, err, name)
		assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusPass), step(gate.StepTests, gate.StatusUnknown)}, result.Steps, name)
		assert.False(t, result.Passed, name)
	}
}

func TestSteps_AFailedCommandLeavesTheTestsStepUnknownBecauseTheReportIsNotRead(t *testing.T) {
	t.Parallel()

	result, err := runWithReports(t, 1, "r/*.xml", map[string]string{"r/a.xml": rootSuite})

	require.NoError(t, err)
	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusFail), step(gate.StepTests, gate.StatusUnknown)}, result.Steps)
	assert.False(t, result.Passed)
}

func TestSteps_AHostJobGivesOneStepFromItsState(t *testing.T) {
	t.Parallel()

	for state, want := range map[model.JobState]gate.Status{model.JobSucceeded: gate.StatusPass, model.JobFailed: gate.StatusFail} {
		g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, &fakeRemote{state: state})

		result, err := g.Run(context.Background(), hostRequest(project("cluster")))

		require.NoError(t, err)
		assert.Equal(t, []gate.Step{step(gate.StepCommand, want)}, result.Steps)
	}
}
