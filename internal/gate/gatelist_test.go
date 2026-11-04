package gate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

func listProject(buildHost string) model.Project {
	return model.Project{Name: "demo", BuildHost: buildHost, Repo: "owner/name", GateSteps: []model.GateSpec{
		{Name: "fmt", Command: "make fmt"}, {Name: "lint", Command: "make lint"}, {Name: "test", Command: "make test"},
	}}
}

// codesByCommand is a shell that gives each command its own exit code and records the commands that ran, in order.
func codesByCommand(codes map[string]int, ran *[]string) gate.Shell {
	return func(_ context.Context, _, command string) (string, int) {
		*ran = append(*ran, command)

		return "out of " + command + "\n", codes[command]
	}
}

func runList(t *testing.T, codes map[string]int) (gate.Result, []string) {
	t.Helper()

	var ran []string

	g := gate.New(codesByCommand(codes, &ran), (&fakeTree{states: []gate.State{clean(headA)}}).read, nil)

	result, err := g.Run(context.Background(), gate.Request{Project: listProject("local"), Dir: "/w"})
	require.NoError(t, err)

	return result, ran
}

func TestSteps_AGateListGivesOneStepForEachSpecAndAFailedStepDoesNotStopTheLaterOnes(t *testing.T) {
	t.Parallel()

	result, ran := runList(t, map[string]int{"make lint": 1})

	assert.Equal(t, []string{"make fmt", "make lint", "make test"}, ran)
	assert.Equal(t, []gate.Step{step("fmt", gate.StatusPass), step("lint", gate.StatusFail), step("test", gate.StatusPass)}, result.Steps)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "== lint ==")
}

func TestSteps_AGateListThatPassesEveryStepPasses(t *testing.T) {
	t.Parallel()

	result, _ := runList(t, nil)

	assert.True(t, result.Passed)
	assert.Len(t, result.Steps, 3)
}

func TestSteps_AGateListKilledByASignalGivesEveryStepUnknownAndTheLaterStepsDoNotRun(t *testing.T) {
	t.Parallel()

	result, ran := runList(t, map[string]int{"make lint": 137})

	assert.Equal(t, []string{"make fmt", "make lint"}, ran)
	assert.Equal(t, []gate.Step{step("fmt", gate.StatusUnknown), step("lint", gate.StatusUnknown), step("test", gate.StatusUnknown)}, result.Steps)
	assert.False(t, result.Passed)
}

func TestSteps_TheNamesOfAGateListEndWithTheTestsStepWhenTheProjectSetsATestReport(t *testing.T) {
	t.Parallel()

	p := listProject("local")
	assert.Equal(t, []string{"fmt", "lint", "test"}, gate.StepNames(p))

	p.TestReport = "r/*.xml"
	assert.Equal(t, []string{"fmt", "lint", "test", gate.StepTests}, gate.StepNames(p))

	assert.Equal(t, []string{gate.StepCommand}, gate.StepNames(project("local")))
}

func TestKey_AnotherGateListGivesAnotherKey(t *testing.T) {
	t.Parallel()

	a, b := listProject("local"), listProject("local")
	b.GateSteps = b.GateSteps[:2]

	assert.NotEqual(t, keyOf(t, clean(headA), &fakeShell{}, a).ID, keyOf(t, clean(headA), &fakeShell{}, b).ID)
}

func TestHostCommand_AGateListWritesOneMarkerForEachStepAndAFailedStepFailsTheJobAfterTheLaterStepsRan(t *testing.T) {
	t.Parallel()

	p := listProject("gpu-a")
	p.GateSteps = []model.GateSpec{{Name: "one", Command: "exit 3"}, {Name: "two", Command: `touch "$HOME/gate-ran"`}}

	code, _, ran, out := runProjectCommand(t, p, "SHIM_HEAD=a1b2c3")

	assert.NotZero(t, code)
	assert.True(t, ran, "the step after a failed step still runs")
	assert.Contains(t, out, "beehave-gate-step one rc=3")
	assert.Contains(t, out, "beehave-gate-step two rc=0")
}

func TestHostCommand_AStepThatEndsItsOutputWithNoNewlineStillGetsAMarkerOnALineOfItsOwn(t *testing.T) {
	t.Parallel()

	p := listProject("gpu-a")
	p.GateSteps = []model.GateSpec{{Name: "one", Command: "printf 'no newline'; exit 2"}}

	_, _, _, out := runProjectCommand(t, p, "SHIM_HEAD=a1b2c3")

	assert.Contains(t, strings.Split(out, "\n"), "beehave-gate-step one rc=2")
}

func TestVerdict_AStepMarkerThatTheLogHoldsTwiceIsUnknownSoALaterLineCannotTurnAFailIntoAPass(t *testing.T) {
	t.Parallel()

	log := strings.Join([]string{"beehave-gate-step fmt rc=0", "beehave-gate-step lint rc=2", "beehave-gate-step lint rc=0", "beehave-gate-step test rc=0"}, "\n")

	result := hostListResult(t, model.JobFailed, log, nil)

	assert.Equal(t, []gate.Step{step("fmt", gate.StatusPass), step("lint", gate.StatusUnknown), step("test", gate.StatusPass)}, result.Steps)
	assert.False(t, result.Passed)
}

func TestHostCommand_AGateListThatPassesEveryStepExitsZero(t *testing.T) {
	t.Parallel()

	p := listProject("gpu-a")
	p.GateSteps = []model.GateSpec{{Name: "one", Command: "true"}, {Name: "two", Command: "true"}}

	code, _, _, _ := runProjectCommand(t, p, "SHIM_HEAD=a1b2c3")

	assert.Zero(t, code)
}

func hostListResult(t *testing.T, state model.JobState, log string, logErr error) gate.Result {
	t.Helper()

	remote := logRemote{fakeRemote: &fakeRemote{state: state}, log: log, logErr: logErr}
	g := gate.New((&fakeShell{}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	result, err := g.Run(context.Background(), hostRequest(listProject("gpu-a")))
	require.NoError(t, err)

	return result
}

func TestVerdict_AFailedHostJobOfAGateListReadsEachStepFromItsMarkerAndAStepWithNoMarkerIsUnknown(t *testing.T) {
	t.Parallel()

	log := strings.Join([]string{"beehave-gate-step fmt rc=0", "beehave-gate-step lint rc=2", "beehave-gate-done"}, "\n")

	result := hostListResult(t, model.JobFailed, log, nil)

	assert.Equal(t, []gate.Step{step("fmt", gate.StatusPass), step("lint", gate.StatusFail), step("test", gate.StatusUnknown)}, result.Steps)
	assert.False(t, result.Passed)
}

func TestVerdict_AStepMarkerWithACodeThatIsNotANumberIsAFailure(t *testing.T) {
	t.Parallel()

	result := hostListResult(t, model.JobFailed, "beehave-gate-step fmt rc=x\nbeehave-gate-step lint rc=0\n", nil)

	assert.Equal(t, []gate.Step{step("fmt", gate.StatusFail), step("lint", gate.StatusPass), step("test", gate.StatusUnknown)}, result.Steps)
}

func TestSubmit_AHostGateWhosePinFileCannotBeReadStartsNoJob(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := gate.New((&fakeShell{output: "fatal\n", code: 128}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	job, err := g.Submit(context.Background(), hostRequest(pinnedHostProject()))

	require.NoError(t, err)
	assert.Empty(t, job.ID)
	assert.Empty(t, remote.submitted)
}

func TestVerdict_AFailedHostJobOfAGateListWhoseLogCannotBeReadGivesEveryStepUnknownAndNeverAPass(t *testing.T) {
	t.Parallel()

	result := hostListResult(t, model.JobFailed, "", assert.AnError)

	assert.Equal(t, []gate.Step{step("fmt", gate.StatusUnknown), step("lint", gate.StatusUnknown), step("test", gate.StatusUnknown)}, result.Steps)
	assert.False(t, result.Passed)
}

func TestVerdict_ASucceededHostJobOfAGateListPassesEveryStep(t *testing.T) {
	t.Parallel()

	result := hostListResult(t, model.JobSucceeded, "", nil)

	assert.True(t, result.Passed)
	assert.Equal(t, []gate.Step{step("fmt", gate.StatusPass), step("lint", gate.StatusPass), step("test", gate.StatusPass)}, result.Steps)
}

func TestRun_AResumedHostJobOfAGateListJudgesTheListItWasSubmittedWithNotTheListOfTheConfigNow(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA), clean(headA)}}, remote)
	submitted := listProject("gpu-a")
	submitted.GateSteps = submitted.GateSteps[:2]

	job, err := g.Submit(context.Background(), hostRequest(submitted))
	require.NoError(t, err)

	// The config changed while the run was down: a step was added to the list.
	request := hostRequest(listProject("gpu-a"))
	request.Resume, request.ResumeHead, request.Plan = job.ID, job.Head, job.Plan

	result, err := g.Run(context.Background(), request)

	require.NoError(t, err)
	assert.True(t, result.Passed)
	assert.Equal(t, []gate.Step{step("fmt", gate.StatusPass), step("lint", gate.StatusPass)}, result.Steps, "the step that the job never ran is not reported")
	assert.Equal(t, []string{"fmt", "lint"}, job.Plan.Required())
}

func TestPlan_AGateStringAsksForNoListAndATestReportAddsTheTestsStep(t *testing.T) {
	t.Parallel()

	assert.Empty(t, gate.PlanOf(project("gpu-a")).Required())

	p := listProject("gpu-a")
	p.TestReport = "r/*.xml"

	assert.Equal(t, []string{"fmt", "lint", "test", gate.StepTests}, gate.PlanOf(p).Required())
}
