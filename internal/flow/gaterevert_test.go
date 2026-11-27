package flow

import (
	"encoding/json"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

// revertChecks are clean checks whose revert run found the names passing on the base code, count of them in all.
func revertChecks(count int, names ...string) *GateChecks {
	checks := cleanChecks()
	checks.Revert, checks.RevertPassed, checks.RevertPassedCount = model.RevertFail, names, count

	return checks
}

func TestStatus_TheGateStepLineShowsTheRevertVerdict(t *testing.T) {
	t.Parallel()

	line := func(name string, checks *GateChecks) StepLine {
		data, err := json.Marshal(stepResult{Outputs: gateOutputs(true, "h1", "t1", "", stepsPass, nil, checks)})
		require.NoError(t, err)

		return callLine([]dbos.StepInfo{{StepName: name, Output: string(data)}}, name)
	}

	assert.Equal(t, model.RevertPass, line("gate:g#1", cleanChecks()).Revert)
	assert.Equal(t, model.RevertFail, line("gate:g#2", revertChecks(1, "pkg TestA")).Revert)
	assert.Equal(t, model.RevertFail, line("gate:g#3", &GateChecks{}).Revert, "a candidate gate with no revert run fails")
	assert.Equal(t, model.RevertFail, line("gate:g#4", nil).Revert, "a gate with no candidate checks proves nothing")

	data, err := json.Marshal(line("gate:g#1", cleanChecks()))
	require.NoError(t, err)
	assert.Contains(t, string(data), `"revert":"pass"`)
}

func TestGate_TheGateResultCarriesTheRevertVerdict(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "pass", gateOutputs(true, "h", "t", "", nil, nil, cleanChecks())[revertField])
	assert.Equal(t, "fail", gateOutputs(true, "h", "t", "", nil, nil, nil)[revertField], "a gate with no candidate checks proves nothing")
}
