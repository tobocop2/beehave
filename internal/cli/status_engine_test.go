package cli_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
)

func TestStatus_NamesTheServeEngineThatHoldsTheLock(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.engine = flow.EngineInfo{Mode: flow.ModeServe, PID: 4242, Since: time.Date(2026, 11, 4, 8, 30, 0, 0, time.UTC)}

	code, stdout, stderr := rig.execute(t.Context(), "status")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "engine: serve, pid 4242, since 2026-11-04T08:30:00Z\n")
	assert.NotContains(t, stdout, "no engine runs")
}

func TestStatus_NamesTheRunEngineWithItsPidOnly(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.engine = flow.EngineInfo{Mode: flow.ModeRun, PID: 77, Since: time.Date(2026, 11, 4, 8, 30, 0, 0, time.UTC)}

	_, stdout, _ := rig.execute(t.Context(), "status")

	assert.Contains(t, stdout, "engine: run, pid 77\n")
	assert.NotContains(t, stdout, "since")
}

func TestStatus_AnEngineThatIsShuttingDownHasNoModeToShow(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true

	_, stdout, _ := rig.execute(t.Context(), "status")

	assert.Contains(t, stdout, "engine: running\n")
}

func TestStatus_SaysNoEngineRunsWhenNoneHoldsTheLock(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.engine = flow.EngineInfo{Mode: flow.ModeServe, PID: 4242}

	_, stdout, _ := rig.execute(t.Context(), "status")

	assert.Contains(t, stdout, "no engine runs\n")
	assert.NotContains(t, stdout, "engine: serve", "a description with no lock is not an engine")
}

func TestStatus_JSONCarriesTheEngineOnlyWhileOneRuns(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.running = true
	rig.steering.engine = flow.EngineInfo{Mode: flow.ModeServe, PID: 4242, Since: time.Date(2026, 11, 4, 8, 30, 0, 0, time.UTC)}

	_, stdout, _ := rig.execute(t.Context(), "status", "--json")

	assert.Contains(t, stdout, `"engine":{"mode":"serve","pid":4242,"since":"2026-11-04T08:30:00Z"}`)

	rig.steering.running = false

	_, stdout, _ = rig.execute(t.Context(), "status", "--json")

	assert.NotContains(t, stdout, `"engine"`)
}

func TestStatus_AnEngineReadThatFailsStopsTheList(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.runningErr = errFake

	code, _, stderr := rig.execute(t.Context(), "status")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, errFake.Error())
}
