package gate_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/model"
)

func pinnedHostProject() model.Project {
	p := hostProject()
	p.PinFile, p.BaseBranch = ".tool-versions", "main"

	return p
}

func TestPins_AHostGateWithAPinFileIsVoidWhenTheHostToolIsAtAnotherVersion(t *testing.T) {
	t.Parallel()

	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobFailed}, log: "beehave-gate-void gofake 1.27 gofake version 1.26.0\nbeehave-gate-done\n"}
	g := gate.New((&fakeShell{output: "gofake 1.27\n"}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })
	p := pinnedHostProject()
	p.TestReport = "r/*.xml"

	result, err := g.Run(context.Background(), hostRequest(p))

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown), step(gate.StepTests, gate.StatusUnknown)}, result.Steps)
	assert.Contains(t, result.Report, "the tool gofake pins version 1.27, but the tool says: gofake version 1.26.0")
	assert.Contains(t, result.Report, "Install the pinned version on gpu-a")
}

func TestPins_AHostGateWithAPinFileAndNoTestReportIsVoidWhenTheHostToolIsAtAnotherVersion(t *testing.T) {
	t.Parallel()

	remote := logRemote{fakeRemote: &fakeRemote{state: model.JobFailed}, log: "beehave-gate-void gofake 1.27 gofake version 1.26.0\nbeehave-gate-done\n"}
	g := gate.New((&fakeShell{output: "gofake 1.27\n"}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })
	p := pinnedHostProject()

	require.Empty(t, p.TestReport)
	require.Empty(t, p.GateSteps)

	result, err := g.Run(context.Background(), hostRequest(p))

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown)}, result.Steps)
	assert.Contains(t, result.Report, "the gate did not run")
	assert.Contains(t, result.Report, "Install the pinned version on gpu-a")
}

func TestPins_AHostGateWithAPinFileThatCannotBeReadSubmitsNoJobAndIsVoid(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := gate.New((&fakeShell{output: "fatal: no such path\n", code: 128}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })

	result, err := g.Run(context.Background(), hostRequest(pinnedHostProject()))

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Empty(t, remote.submitted)
	assert.Equal(t, []gate.Step{step(gate.StepCommand, gate.StatusUnknown)}, result.Steps)
}

func hostGateOn(shell *fakeShell, remote *fakeRemote) gate.Gate {
	return gate.New(shell.run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })
}

func TestPins_AVoidJobStaysVoidWhenTheConfigLosesItsPinFileBeforeTheFinish(t *testing.T) {
	t.Parallel()

	submitting := hostGateOn(&fakeShell{output: "fatal: no such path\n", code: 128}, &fakeRemote{state: model.JobSucceeded})
	job, err := submitting.Submit(context.Background(), hostRequest(pinnedHostProject()))

	require.NoError(t, err)
	require.Empty(t, job.ID, "a pin file that cannot be read starts no job")

	// The config was edited while the run was down: pin_file is gone, and the pin file would now read fine.
	remote := &fakeRemote{state: model.JobSucceeded}
	request := hostRequest(hostProject())
	request.Resume, request.ResumeHead, request.Plan = job.ID, job.Head, job.Plan

	result, err := hostGateOn(&fakeShell{output: "gofake 1.27\n"}, remote).Run(context.Background(), request)

	require.NoError(t, err)
	assert.False(t, result.Passed)
	assert.Contains(t, result.Report, "the gate did not run")
	assert.Empty(t, remote.submitted, "the finish submits no new job with no pin check")
	assert.Empty(t, remote.waited)
}

func TestPins_AGoodJobIsNotVoidedWhenThePinFileIsMissingByTheFinish(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	job, err := hostGateOn(&fakeShell{output: "gofake 1.27\n"}, remote).Submit(context.Background(), hostRequest(pinnedHostProject()))

	require.NoError(t, err)
	require.Equal(t, model.JobID("job-7"), job.ID)

	// The config now names a pin file that the base commit does not hold.
	moved := pinnedHostProject()
	moved.PinFile, moved.BaseBranch = "gone/.tool-versions", "other"
	request := hostRequest(moved)
	request.Resume, request.ResumeHead, request.Plan = job.ID, job.Head, job.Plan
	finishing := &fakeRemote{state: model.JobSucceeded}

	result, err := hostGateOn(&fakeShell{output: "fatal: no such path\n", code: 128}, finishing).Run(context.Background(), request)

	require.NoError(t, err)
	assert.True(t, result.Passed, "the job that passed is judged by the pin check of its own submit")
	assert.Equal(t, []model.JobID{"job-7"}, finishing.waited)
	assert.Empty(t, finishing.submitted)
}

func TestPins_AFinishJudgesTheBuildHostOfThePlanAndNotTheOneOfTheConfigNow(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	job, err := hostGateOn(&fakeShell{}, remote).Submit(context.Background(), hostRequest(project("gpu-a")))
	require.NoError(t, err)

	var asked []string

	g := gate.New((&fakeShell{}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(p model.Project) (gate.Remote, error) {
		asked = append(asked, p.BuildHost)

		return remote, nil
	})
	request := hostRequest(project("gpu-b"))
	request.Resume, request.ResumeHead, request.Plan = job.ID, job.Head, job.Plan

	result, err := g.Run(context.Background(), request)

	require.NoError(t, err)
	assert.True(t, result.Passed)
	assert.Equal(t, []string{"gpu-a"}, asked, "the host comes from the plan")
}

func TestPins_TheHostCommandProbesThePinnedToolBeforeTheGateAndAToolAtAnotherVersionStopsTheGate(t *testing.T) {
	t.Parallel()

	home, bin := t.TempDir(), t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), gitShim))

	remote := &fakeRemote{state: model.JobSucceeded, root: home}
	g := gate.New((&fakeShell{output: "git 2.99.0\n"}).run, (&fakeTree{states: []gate.State{clean(headA)}}).read, func(model.Project) (gate.Remote, error) { return remote, nil })
	p := pinnedHostProject()
	p.Gate = `touch "$HOME/gate-ran"`

	_, err := g.Run(context.Background(), hostRequest(p))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)

	// The shim git prints nothing for `git version`, so the host tool is not at the pinned version.
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", remote.submitted[0].Command) //nolint:gosec // the command is the one the code under test composes, run against a shim git
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin", "SHIM_HEAD=a1b2c3"}
	out, runErr := cmd.Output()

	var exit *exec.ExitError
	require.ErrorAs(t, runErr, &exit)
	assert.NotZero(t, exit.ExitCode())
	assert.Contains(t, string(out), "beehave-gate-void git 2.99.0")
	assert.NoFileExists(t, filepath.Join(home, "gate-ran"), "the gate command must not run")
}
