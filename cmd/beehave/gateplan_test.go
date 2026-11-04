package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	igate "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

// The plan that gate:job recorded is the only source of the gate list, the build host and the pin check at the finish. The adapter must hand it to the gate.
func TestProjectGate_RunJudgesTheRecordedPlanAndNotTheGateOfTheConfigNow(t *testing.T) {
	t.Parallel()

	calls := &gateCalls{}
	gate := projectGate{
		projects: localProject(),
		programs: programs{trees: treeAt("/tree"), local: gateRunner(calls, model.CommandResult{Stdout: "abc123\n"}, model.CommandResult{Stdout: "ok"})},
	}
	recorded := igate.Plan{
		Steps:     []model.GateSpec{{Name: "fmt", Command: "recorded-fmt"}, {Name: "lint", Command: "recorded-lint"}},
		BuildHost: config.LocalHost,
		Pins:      &igate.PinCheck{},
	}

	got, err := gate.Run(t.Context(), "p", "1", "", flow.GateJob{Plan: recorded})

	require.NoError(t, err)
	assert.Equal(t, []flow.GateStep{{Name: "fmt", Status: "pass"}, {Name: "lint", Status: "pass"}}, got.Steps, "the steps are the steps of the recorded list, not the make check of the config")
	assert.Equal(t, []string{"-c", "recorded-fmt"}, calls.args[2])
	assert.Equal(t, []string{"-c", "recorded-lint"}, calls.args[3])
}
