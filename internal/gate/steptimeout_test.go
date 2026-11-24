package gate_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

func timeoutProject(buildHost string) model.Project {
	return model.Project{Name: "demo", BuildHost: buildHost, Repo: "owner/name", GateSteps: []model.GateSpec{
		{Name: "build", Command: "make build", Timeout: 20 * time.Millisecond}, {Name: "test", Command: "make test"},
	}}
}

// hangingFirst is a shell whose command "make build" hangs until its context ends, as a killed command does, and whose other commands exit 0.
func hangingFirst(ran *[]string) gate.Shell {
	return func(ctx context.Context, _, command string) (string, int) {
		*ran = append(*ran, command)

		if command == "make build" {
			<-ctx.Done()

			return "building\n", gate.ExitTimedOut
		}

		return "ok\n", 0
	}
}

func TestSteps_AStepThatRunsPastItsOwnTimeoutIsUnknownAndTheNextStepRuns(t *testing.T) {
	t.Parallel()

	var ran []string

	g := gate.New(hangingFirst(&ran), (&fakeTree{states: []gate.State{clean(headA)}}).read, nil)

	result, err := g.Run(context.Background(), gate.Request{Project: timeoutProject("local"), Dir: "/w"})

	require.NoError(t, err)
	assert.Equal(t, []string{"make build", "make test"}, ran)
	assert.Equal(t, []gate.Step{step("build", gate.StatusUnknown), step("test", gate.StatusPass)}, result.Steps)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "the step build ran past its timeout of 20ms")
	assert.Contains(t, result.Report, "(timeout)")
}

func TestSteps_AStepThatRunsPastItsTimeoutLeavesTheTestsStepUnknown(t *testing.T) {
	t.Parallel()

	var ran []string

	project := timeoutProject("local")
	project.TestReport = "r/*.xml"

	g := gate.New(hangingFirst(&ran), (&fakeTree{states: []gate.State{clean(headA)}}).read, nil)

	result, err := g.Run(context.Background(), gate.Request{Project: project, Dir: "/w"})

	require.NoError(t, err)
	assert.Equal(t, []gate.Step{step("build", gate.StatusUnknown), step("test", gate.StatusPass), step(gate.StepTests, gate.StatusUnknown)}, result.Steps)
}

func TestSteps_ALimitOfTheWholeGateStillStopsTheLaterStepsOfAListWithStepTimeouts(t *testing.T) {
	t.Parallel()

	var ran []string

	shell := func(_ context.Context, _, command string) (string, int) {
		ran = append(ran, command)

		return "partial\n", gate.ExitTimedOut
	}
	g := gate.New(shell, (&fakeTree{states: []gate.State{clean(headA)}}).read, nil)

	result, err := g.Run(context.Background(), gate.Request{Project: timeoutProject("local"), Dir: "/w"})

	require.NoError(t, err)
	assert.Equal(t, []string{"make build"}, ran)
	assert.Equal(t, []gate.Step{step("build", gate.StatusUnknown), step("test", gate.StatusUnknown)}, result.Steps)
	assert.Contains(t, result.Report, "ran past its time limit")
}

func TestHostCommand_AStepWithATimeoutIsKilledAtItsLimitAndTheNextStepStillRuns(t *testing.T) {
	t.Parallel()

	p := listProject("gpu-a")
	p.GateSteps = []model.GateSpec{{Name: "one", Command: "sleep 30", Timeout: time.Second}, {Name: "two", Command: `touch "$HOME/gate-ran"`}}

	code, _, ran, out := runProjectCommand(t, p, "SHIM_HEAD=a1b2c3")

	assert.NotZero(t, code)
	assert.True(t, ran, "the step after the timed-out step runs")
	assert.Contains(t, out, "beehave-gate-step one rc=124")
	assert.Contains(t, out, "beehave-gate-step two rc=0")
}

func TestHostCommand_AStepThatIgnoresTERMIsKilledAfterTheGraceAndTheNextStepStillRuns(t *testing.T) {
	t.Parallel()

	p := listProject("gpu-a")
	p.GateSteps = []model.GateSpec{{Name: "one", Command: "trap '' TERM; sleep 20", Timeout: time.Second}, {Name: "two", Command: `touch "$HOME/gate-ran"`}}

	start := time.Now()
	code, _, ran, out := runProjectCommand(t, p, "SHIM_HEAD=a1b2c3")

	assert.Less(t, time.Since(start), 15*time.Second, "the step ends after the grace, not when its sleep ends")
	assert.NotZero(t, code)
	assert.True(t, ran, "the step after the killed step runs")
	assert.Contains(t, out, "beehave-gate-step one rc=137")
	assert.Contains(t, out, "beehave-gate-step two rc=0")
}

func TestVerdict_AHostStepWithATimeoutThatTheMarkerGivesAs137IsUnknownWithTheReasonTimeout(t *testing.T) {
	t.Parallel()

	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobFailed}, log: "beehave-gate-step build rc=137\nbeehave-gate-step test rc=0\n"}
	g := gate.New((&fakeShell{}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	result, err := g.Run(context.Background(), hostRequest(timeoutProject("gpu-a")))

	require.NoError(t, err)
	assert.Equal(t, []gate.Step{step("build", gate.StatusUnknown), step("test", gate.StatusPass)}, result.Steps)
	assert.Contains(t, result.Report, "the step build ran past its timeout of 20ms")
}

func TestVerdict_AHostStepWithNoTimeoutThatExits137IsAFailure(t *testing.T) {
	t.Parallel()

	result := hostListResult(t, model.JobFailed, "beehave-gate-step fmt rc=137\nbeehave-gate-step lint rc=0\nbeehave-gate-step test rc=0\n", nil)

	assert.Equal(t, []gate.Step{step("fmt", gate.StatusFail), step("lint", gate.StatusPass), step("test", gate.StatusPass)}, result.Steps)
}

func TestSteps_WhenTheStepLimitAndTheLimitOfTheWholeGateBothFireTheGateStopsAndEveryStepIsUnknown(t *testing.T) {
	t.Parallel()

	whole, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	var ran []string

	shell := func(ctx context.Context, _, command string) (string, int) {
		ran = append(ran, command)

		<-ctx.Done()
		<-whole.Done() // both deadlines have passed when the shell returns

		return "building\n", gate.ExitTimedOut
	}
	g := gate.New(shell, (&fakeTree{states: []gate.State{clean(headA)}}).read, nil)

	result, err := g.Run(whole, gate.Request{Project: timeoutProject("local"), Dir: "/w"})

	require.NoError(t, err)
	assert.Equal(t, []string{"make build"}, ran, "the later step does not run")
	assert.Equal(t, []gate.Step{step("build", gate.StatusUnknown), step("test", gate.StatusUnknown)}, result.Steps)
	assert.Contains(t, result.Report, "ran past its time limit")
	assert.NotContains(t, result.Report, "(timeout)")
}

func TestVerdict_AHostStepWithATimeoutThatTheMarkerGivesAs124IsUnknownWithTheReasonTimeout(t *testing.T) {
	t.Parallel()

	log := "beehave-gate-step build rc=124\nbeehave-gate-step test rc=0\n"
	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobFailed}, log: log}
	g := gate.New((&fakeShell{}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	result, err := g.Run(context.Background(), hostRequest(timeoutProject("gpu-a")))

	require.NoError(t, err)
	assert.Equal(t, []gate.Step{step("build", gate.StatusUnknown), step("test", gate.StatusPass)}, result.Steps)
	assert.Contains(t, result.Report, "the step build ran past its timeout of 20ms")
	assert.NotContains(t, result.Report, "the step test")
}

func TestVerdict_AHostStepWithNoTimeoutThatExits124IsAFailure(t *testing.T) {
	t.Parallel()

	result := hostListResult(t, model.JobFailed, "beehave-gate-step fmt rc=124\nbeehave-gate-step lint rc=0\nbeehave-gate-step test rc=0\n", nil)

	assert.Equal(t, []gate.Step{step("fmt", gate.StatusFail), step("lint", gate.StatusPass), step("test", gate.StatusPass)}, result.Steps)
	assert.NotContains(t, result.Report, "timeout")
}

func TestKey_AnotherStepTimeoutGivesAnotherKey(t *testing.T) {
	t.Parallel()

	a, b := timeoutProject("local"), timeoutProject("local")
	b.GateSteps = []model.GateSpec{{Name: "build", Command: "make build", Timeout: time.Hour}, b.GateSteps[1]}

	assert.NotEqual(t, keyOf(t, clean(headA), &fakeShell{}, a).ID, keyOf(t, clean(headA), &fakeShell{}, b).ID)
}
