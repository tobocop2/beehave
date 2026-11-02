package cli_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/flow"
)

func intPtr(n int) *int { return &n }

func boolPtr(b bool) *bool { return &b }

// stepsView is a finished step, a failed step and a step that runs.
func stepsView() flow.RunSteps {
	start := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)

	return flow.RunSteps{Run: "item-14-r1", State: "running", Steps: []flow.StepLine{
		{
			Name: "agent:work#1", State: "done", Started: start, Ended: start.Add(15 * time.Minute), Seconds: 900,
			ExitCode: intPtr(0), Verdict: "approve", Head: "61e7998",
		},
		{
			Name: "gate:g#1", State: "done", Started: start.Add(15 * time.Minute), Ended: start.Add(22 * time.Minute), Seconds: 408,
			Passed: boolPtr(false), Gate: []flow.GateStep{{Name: "command", Status: "pass"}, {Name: "tests", Status: "fail"}},
		},
		{Name: "command:c#1", State: "failed", Started: start.Add(22 * time.Minute), Ended: start.Add(22 * time.Minute), Seconds: 0.25, Error: "the lint\nprogram is missing"},
		{Name: "agent:fix#1", State: "running", Started: start.Add(23 * time.Minute), Seconds: 125, Stop: "turns"},
	}}
}

func TestStatus_ARunIdListsTheStepsOneLineEach(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.view = stepsView()

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, []string{"steps item-14-r1"}, rig.steering.calls)
	assert.Equal(t, "agent:work#1\t2026-10-31T09:00:00Z\t15m0s\tdone\texit_code=0 verdict=approve head=61e7998\n"+
		"gate:g#1\t2026-10-31T09:15:00Z\t6m48s\tdone\tgate=fail steps=command:pass,tests:fail\n"+
		"command:c#1\t2026-10-31T09:22:00Z\t250ms\tfailed\terror=\"the lint program is missing\"\n"+
		"agent:fix#1\t2026-10-31T09:23:00Z\t2m5s\trunning\tstop=turns\n"+
		"run\trunning\n", stdout)
	assert.True(t, rig.steering.closeOK)
}

func TestStatus_AnAgentLineSaysWhenTheAgentRewroteTheHistory(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	start := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	rig.steering.view = flow.RunSteps{Run: "item-14-r1", State: "done", Steps: []flow.StepLine{
		{Name: "agent:fix#1", State: "done", Started: start, Ended: start, Head: "9bbc2f5", Rewrote: true},
	}}

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "agent:fix#1\t2026-10-31T09:00:00Z\t0s\tdone\thead=9bbc2f5 rewrote=yes\nrun\tdone\n", stdout)
}

func TestStatus_AnAgentLineOfARunNamesItsKindAsTheLaneLedger(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	start := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	rig.steering.view = flow.RunSteps{Run: "item-14-r1", State: "done", Steps: []flow.StepLine{
		{Name: "agent:work#1", State: "done", Started: start, Ended: start, Kind: "rework", ExitCode: intPtr(0)},
		{Name: "agent:look#1", State: "done", Started: start, Ended: start, Kind: "review", ExitCode: intPtr(0)},
	}}

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "agent:work#1\t2026-10-31T09:00:00Z\t0s\tdone\texit_code=0 kind=rework\n"+
		"agent:look#1\t2026-10-31T09:00:00Z\t0s\tdone\texit_code=0 kind=review\n"+
		"run\tdone\n", stdout)

	code, stdout, stderr = rig.execute(t.Context(), "status", "item-14-r1", "--json")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, `"kind":"review"`)
}

func TestStatus_AnItemListsItsLanesAcrossItsRunsOneLineEach(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	start := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	rig.steering.lanes = flow.ItemLanes{Item: "14", Lanes: []flow.LaneLine{
		{Run: "item-14-r1", StepLine: flow.StepLine{Name: "agent:work#1", State: "done", Started: start, Kind: "fix", ExitCode: intPtr(0)}},
		{Run: "item-14-r2", StepLine: flow.StepLine{Name: "agent:work#1", State: "done", Started: start, Kind: "rework", ExitCode: intPtr(0)}},
	}}

	code, stdout, stderr := rig.execute(t.Context(), "status", "--item", "14")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "item-14-r1\tagent:work#1\t2026-10-31T09:00:00Z\t0s\tdone\texit_code=0 kind=fix\n"+
		"item-14-r2\tagent:work#1\t2026-10-31T09:00:00Z\t0s\tdone\texit_code=0 kind=rework\n", stdout)
	assert.Equal(t, []string{"lanes 14"}, rig.steering.calls)

	code, stdout, stderr = rig.execute(t.Context(), "status", "--item", "14", "--json")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, `"item":"14"`)
	assert.Contains(t, stdout, `"run":"item-14-r2"`)
	assert.Contains(t, stdout, `"kind":"rework"`)
}

func TestStatus_AnItemAndARunIdTogetherAreRefused(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "status", "--item", "14", "item-14-r1")

	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "give a run id or --item, not both")
	assert.Empty(t, rig.steering.calls)
}

func TestStatus_AFailedRunEndsWithTheErrorOfTheRun(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.view = flow.RunSteps{Run: "item-14-r1", State: "failed", Error: "workflow.star:3: fail: boom\nmore"}

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "run\tfailed\terror=\"workflow.star:3: fail: boom more\"\n", stdout)

	code, stdout, stderr = rig.execute(t.Context(), "status", "item-14-r1", "--json")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, `"error":"workflow.star:3: fail: boom\nmore"`)
}

func TestStatus_APassingGateShowsPassAndAnEmptyStepList(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.view = flow.RunSteps{Run: "item-14-r1", State: "done", Steps: []flow.StepLine{
		{Name: "gate:g#1", State: "done", Started: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC), Seconds: 3, Passed: boolPtr(true)},
	}}

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "gate:g#1\t2026-10-31T09:00:00Z\t3s\tdone\tgate=pass steps=\nrun\tdone\n", stdout)
}

func TestStatus_AStepWhoseOutputCannotBeReadSaysSo(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.view = flow.RunSteps{Run: "item-14-r1", State: "done", Steps: []flow.StepLine{
		{Name: "command:c#1", State: "done", Started: time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC), Seconds: 1, OutputUnreadable: true},
	}}

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "command:c#1\t2026-10-31T09:00:00Z\t1s\tdone\toutput_unreadable\nrun\tdone\n", stdout)

	code, stdout, stderr = rig.execute(t.Context(), "status", "item-14-r1", "--json")

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, `"output_unreadable":true`)
}

func TestStatus_ARunIdWithJSONPrintsOneObjectForScripts(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.view = stepsView()

	code, stdout, stderr := rig.execute(t.Context(), "status", "item-14-r1", "--json")

	require.Equal(t, 0, code, stderr)

	var got struct {
		Run   string `json:"run"`
		State string `json:"state"`
		Steps []struct {
			Name     string  `json:"name"`
			State    string  `json:"state"`
			Seconds  float64 `json:"duration_seconds"`
			ExitCode *int    `json:"exit_code"`
			Verdict  string  `json:"verdict"`
			Head     string  `json:"head"`
			Passed   *bool   `json:"passed"`
			Gate     []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
			} `json:"gate"`
			Ended string `json:"ended"`
		} `json:"steps"`
	}

	require.NoError(t, json.Unmarshal([]byte(stdout), &got), stdout)
	assert.Equal(t, "item-14-r1", got.Run)
	assert.Equal(t, "running", got.State)
	require.Len(t, got.Steps, 4)
	assert.Equal(t, "agent:work#1", got.Steps[0].Name)
	assert.InDelta(t, 900.0, got.Steps[0].Seconds, 0)
	assert.Equal(t, "approve", got.Steps[0].Verdict)
	assert.Equal(t, "61e7998", got.Steps[0].Head)
	assert.Equal(t, 0, *got.Steps[0].ExitCode)
	require.NotNil(t, got.Steps[1].Passed)
	assert.False(t, *got.Steps[1].Passed)
	assert.Equal(t, "tests", got.Steps[1].Gate[1].Name)
	assert.Equal(t, "fail", got.Steps[1].Gate[1].Status)
	assert.Equal(t, "running", got.Steps[3].State)
	assert.Empty(t, got.Steps[3].Ended, "a step that runs has no end time")
}

func TestStatus_JSONWithNoRunIdIsRefusedAndAMissingRunReportsItsError(t *testing.T) {
	t.Parallel()

	rig := newRig(t)

	code, _, stderr := rig.execute(t.Context(), "status", "--json")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "give the run id after status")
	assert.Empty(t, rig.steering.calls)

	rig.steering.err = errFake
	code, _, stderr = rig.execute(t.Context(), "status", "item-99-r1")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")
}

func TestStatus_AnItemWhoseLanesCannotBeReadFails(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.steering.err = errFake

	code, _, stderr := rig.execute(t.Context(), "status", "--item", "14")
	assert.Equal(t, 1, code)
	assert.Contains(t, stderr, "fake failure")
}

func TestRun_GivesTheEngineStderrForTheLinesOfTheRun(t *testing.T) {
	t.Parallel()

	rig := newRig(t)
	rig.progress = "progress line\n"

	code, stdout, stderr := rig.execute(t.Context(), "run", "12")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "progress line\n", stderr)
	assert.NotContains(t, stdout, "progress line")
}
