package cli_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/cli"
	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
)

// fakeMachine is a build host with the three calls the host commands make.
type fakeMachine struct {
	host.Host

	submitted  []model.JobSpec
	submitErr  error
	state      model.JobState
	waitErr    error
	teardownIn int
	tearErr    error
	id         string
	noID       bool
	idErr      error

	// running makes Status report an up host. upIn counts the Up calls, and upErr fails them.
	running bool
	upIn    int
	upErr   error
	statErr error

	beforeTeardown func()
}

func (m *fakeMachine) Status(context.Context) (model.HostStatus, error) {
	if m.statErr != nil {
		return model.HostStatus{}, m.statErr
	}

	if m.running {
		return model.HostStatus{State: model.HostUp}, nil
	}

	return model.HostStatus{State: model.HostAbsent}, nil
}

func (m *fakeMachine) Up(context.Context) error {
	m.upIn++

	if m.upErr == nil {
		m.running = true
	}

	return m.upErr
}

// markerName is the marker of project p in the state folder.
const markerName = "host-p@local"

// ClusterID reports the id c1 unless the test sets another, or sets noID.
func (m *fakeMachine) ClusterID(context.Context) (string, error) {
	if m.id == "" && !m.noID {
		return "c1", m.idErr
	}

	return m.id, m.idErr
}

func (m *fakeMachine) Submit(_ context.Context, spec model.JobSpec) (model.JobID, error) {
	m.submitted = append(m.submitted, spec)

	return "42", m.submitErr
}

func (m *fakeMachine) Wait(context.Context, model.JobID) (model.JobState, error) {
	return m.state, m.waitErr
}

func (m *fakeMachine) Teardown(context.Context) error {
	m.teardownIn++

	if m.beforeTeardown != nil {
		m.beforeTeardown()
	}

	return m.tearErr
}

// hostRig returns a config folder with project p, the fake machine and the build info that opens it.
func hostRig(t *testing.T, machine *fakeMachine) (cli.BuildInfo, map[string]string, string) {
	t.Helper()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.toml", validRunners)
	writeConfig(t, dir, "projects/p.toml", validProject)

	info := cli.BuildInfo{Host: cli.HostDeps{Open: func(project model.Project) (cli.Machine, error) {
		if project.Name != "p" {
			return nil, errors.New("wrong project")
		}

		return machine, nil
	}}}

	return info, map[string]string{}, dir
}

func TestHostSubmit_PrintsTheLaunchedLineAndTheJobAndDoesNotWait(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{}
	info, env, dir := hostRig(t, machine)

	code, stdout, stderr := runCLI(t, env, info, "host", "submit", "p", "sh job.sh", "--config", dir, "--name", "gate", "--log", "/h/log", "--marker", "DONE-1")

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "launched: /h/log, marker DONE-1\n", stdout)
	assert.Contains(t, stderr, "job 42")
	assert.Equal(t, []model.JobSpec{{Name: "gate", Command: "sh job.sh"}}, machine.submitted)
}

func TestHostSubmit_WaitExitsByTheFinalStateOfTheJob(t *testing.T) {
	t.Parallel()

	for state, want := range map[model.JobState]int{model.JobSucceeded: 0, model.JobFailed: 1, model.JobCanceled: 1, model.JobRunning: 2} {
		machine := &fakeMachine{state: state}
		info, env, dir := hostRig(t, machine)

		code, stdout, _ := runCLI(t, env, info, "host", "submit", "p", "sh job.sh", "--config", dir, "--log", "/h/log", "--marker", "M", "--wait")

		assert.Equal(t, want, code, state)

		if want < 2 {
			assert.Contains(t, stdout, "job 42 "+string(state))
		}
	}
}

func TestHostSubmit_RefusesWithoutALogAndAMarker(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{}
	info, env, dir := hostRig(t, machine)

	code, _, stderr := runCLI(t, env, info, "host", "submit", "p", "sh job.sh", "--config", dir)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "--marker")
	assert.Empty(t, machine.submitted)
}

func TestHostSubmit_ASubmitErrorExitsWithARefusal(t *testing.T) {
	t.Parallel()

	info, env, dir := hostRig(t, &fakeMachine{submitErr: errors.New("no slot")})

	code, _, stderr := runCLI(t, env, info, "host", "submit", "p", "sh job.sh", "--config", dir, "--log", "/h/log", "--marker", "M")

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "no slot")
}

func TestHostSubmit_AWaitErrorExitsWithARefusal(t *testing.T) {
	t.Parallel()

	info, env, dir := hostRig(t, &fakeMachine{waitErr: errors.New("ssh down")})

	code, _, stderr := runCLI(t, env, info, "host", "submit", "p", "sh job.sh", "--config", dir, "--log", "/h/log", "--marker", "M", "--wait")

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "ssh down")
}

// started runs host up for project p on a stopped machine, so the state folder in env holds the marker of the cluster.
func started(t *testing.T, info cli.BuildInfo, env map[string]string, dir string) {
	t.Helper()

	env["OBEE_STATE_DIR"] = filepath.Join(t.TempDir(), "state")

	code, _, stderr := runCLI(t, env, info, "host", "up", "p", "--config", dir)

	require.Equal(t, 0, code, stderr)
}

func TestHostUp_StartsAStoppedClusterMarksItAndCleanStopsIt(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{id: "c1"}
	info, env, dir := hostRig(t, machine)
	started(t, info, env, dir)

	assert.Equal(t, 1, machine.upIn)

	marker, err := os.ReadFile(filepath.Join(env["OBEE_STATE_DIR"], markerName))
	require.NoError(t, err)
	assert.Equal(t, "c1", string(marker))

	code, _, stderr := runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, 1, machine.teardownIn)
}

func TestHostUp_LeavesARunningClusterUnmarkedAndCleanRefuses(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{id: "c1", running: true}
	info, env, dir := hostRig(t, machine)
	env["OBEE_STATE_DIR"] = filepath.Join(t.TempDir(), "state")

	code, stdout, stderr := runCLI(t, env, info, "host", "up", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Zero(t, machine.upIn)
	assert.Contains(t, stdout+stderr, "already running")
	assert.Contains(t, stdout+stderr, "will not stop it")
	assert.NoFileExists(t, filepath.Join(env["OBEE_STATE_DIR"], markerName))

	code, _, stderr = runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "obee host up")
	assert.Zero(t, machine.teardownIn)
}

func TestHostUp_ExitsZeroWhenTheMarkerAlreadyNamesTheRunningCluster(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{id: "c1"}
	info, env, dir := hostRig(t, machine)
	started(t, info, env, dir)

	code, stdout, stderr := runCLI(t, env, info, "host", "up", "p", "--config", dir)

	require.Equal(t, 0, code, stderr)
	assert.Contains(t, stdout, "already running")
	assert.Equal(t, 1, machine.upIn)
}

func TestHostUp_WritesNoMarkerWhenUpFails(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{upErr: errors.New("no quota")}
	info, env, dir := hostRig(t, machine)
	env["OBEE_STATE_DIR"] = filepath.Join(t.TempDir(), "state")

	code, _, stderr := runCLI(t, env, info, "host", "up", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "no quota")
	assert.NoFileExists(t, filepath.Join(env["OBEE_STATE_DIR"], markerName))
}

func TestHostUp_FailsWhenTheStatusOrTheClusterIDCannotBeRead(t *testing.T) {
	t.Parallel()

	for _, machine := range []*fakeMachine{{statErr: errors.New("sky down")}, {idErr: errors.New("sky down")}, {noID: true}} {
		info, env, dir := hostRig(t, machine)
		env["OBEE_STATE_DIR"] = filepath.Join(t.TempDir(), "state")

		code, _, _ := runCLI(t, env, info, "host", "up", "p", "--config", dir)

		require.Equal(t, 2, code)
		assert.NoFileExists(t, filepath.Join(env["OBEE_STATE_DIR"], markerName))
	}
}

func TestHostUp_RefusesARunningClusterWhoseIDOrMarkerCannotBeRead(t *testing.T) {
	t.Parallel()

	info, env, dir := hostRig(t, &fakeMachine{running: true, idErr: errors.New("sky down")})
	env["OBEE_STATE_DIR"] = filepath.Join(t.TempDir(), "state")

	code, _, stderr := runCLI(t, env, info, "host", "up", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "sky down")

	info, env, dir = obstructed(t, &fakeMachine{running: true}, markerName)

	code, _, _ = runCLI(t, env, info, "host", "up", "p", "--config", dir)

	require.Equal(t, 2, code)
}

func TestHostUp_RefusesAProjectTheConfigDoesNotName(t *testing.T) {
	t.Parallel()

	info, env, dir := hostRig(t, &fakeMachine{})

	code, _, stderr := runCLI(t, env, info, "host", "up", "other", "--config", dir)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, `no project named "other"`)
}

func TestHostSubmit_WritesNoMarker(t *testing.T) {
	t.Parallel()

	info, env, dir := hostRig(t, &fakeMachine{})
	env["OBEE_STATE_DIR"] = filepath.Join(t.TempDir(), "state")

	code, _, stderr := runCLI(t, env, info, "host", "submit", "p", "sh job.sh", "--config", dir, "--log", "/h/log", "--marker", "M")

	require.Equal(t, 0, code, stderr)
	assert.NoFileExists(t, filepath.Join(env["OBEE_STATE_DIR"], markerName))
}

func TestHostClean_BanksAndStopsTheHostThatUpStarted(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{id: "c1"}
	info, env, dir := hostRig(t, machine)
	started(t, info, env, dir)

	code, stdout, stderr := runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 0, code, stderr)
	assert.Equal(t, "host stopped: p\n", stdout)
	assert.Equal(t, 1, machine.teardownIn)

	code, _, stderr = runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 2, code, "a second clean finds no marker")
	assert.Contains(t, stderr, "did not start")
	assert.Equal(t, 1, machine.teardownIn)
}

func TestHostClean_RefusesWithNoMarkerAndDoesNotTearDown(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{id: "c1"}
	info, env, dir := hostRig(t, machine)

	code, stdout, stderr := runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "did not start")
	assert.Zero(t, machine.teardownIn)
}

func TestHostClean_RefusesAClusterWithAnotherIDAndKeepsTheMarker(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{id: "c1"}
	info, env, dir := hostRig(t, machine)
	started(t, info, env, dir)

	machine.id = "c2"

	code, _, stderr := runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "did not start")
	assert.Zero(t, machine.teardownIn)

	machine.id = "c1"

	code, _, stderr = runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 0, code, stderr)
}

func TestHostClean_KeepsTheMarkerWhenTheTeardownFails(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{id: "c1", tearErr: errors.New("host stays up: unbanked")}
	info, env, dir := hostRig(t, machine)
	started(t, info, env, dir)

	code, stdout, stderr := runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "host stays up")

	machine.tearErr = nil

	code, _, stderr = runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 0, code, stderr)
}

// obstructed returns a state folder that holds a folder at each of the given names, and the rig with it set as the state folder.
func obstructed(t *testing.T, machine *fakeMachine, names ...string) (cli.BuildInfo, map[string]string, string) {
	t.Helper()

	info, env, dir := hostRig(t, machine)
	state := filepath.Join(t.TempDir(), "state")
	env["OBEE_STATE_DIR"] = state

	for _, name := range names {
		require.NoError(t, os.MkdirAll(filepath.Join(state, name), 0o700))
	}

	return info, env, dir
}

func TestHostCommands_RefuseAStateFolderTheyCannotUse(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	for state, want := range map[string]string{"relative/state": "absolute", file: "not a"} {
		for _, verb := range []string{"up", "clean"} {
			info, env, dir := hostRig(t, &fakeMachine{})
			env["OBEE_STATE_DIR"] = state

			code, _, stderr := runCLI(t, env, info, "host", verb, "p", "--config", dir)

			require.Equal(t, 2, code, verb)
			assert.Contains(t, stderr, want, verb)
		}
	}
}

func TestHostUp_FailsWhenTheMarkerCannotBeWritten(t *testing.T) {
	t.Parallel()

	for _, name := range []string{markerName, markerName + ".tmp"} {
		info, env, dir := obstructed(t, &fakeMachine{}, name)

		code, _, _ := runCLI(t, env, info, "host", "up", "p", "--config", dir)

		require.Equal(t, 2, code, name)
	}
}

func TestHostClean_RefusesAMarkerItCannotRead(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{}
	info, env, dir := obstructed(t, machine, markerName)

	code, _, _ := runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Zero(t, machine.teardownIn)
}

func TestHostClean_RefusesWhenTheClusterIDCannotBeRead(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{idErr: errors.New("sky down")}
	info, env, dir := hostRig(t, machine)

	code, _, stderr := runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "sky down")
	assert.Zero(t, machine.teardownIn)
}

func TestHostClean_FailsWhenTheMarkerIsGoneAfterTheTeardown(t *testing.T) {
	t.Parallel()

	machine := &fakeMachine{}
	info, env, dir := hostRig(t, machine)
	started(t, info, env, dir)

	machine.beforeTeardown = func() { require.NoError(t, os.Remove(filepath.Join(env["OBEE_STATE_DIR"], markerName))) }

	code, _, _ := runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Equal(t, 1, machine.teardownIn)
}

func TestHostCommands_RefuseAProjectTheConfigDoesNotName(t *testing.T) {
	t.Parallel()

	info, env, dir := hostRig(t, &fakeMachine{})

	code, _, stderr := runCLI(t, env, info, "host", "clean", "other", "--config", dir)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, `no project named "other"`)
}

func TestHostCommands_RefuseABrokenConfigFolder(t *testing.T) {
	t.Parallel()

	info, env, dir := hostRig(t, &fakeMachine{})
	writeConfig(t, dir, "projects/bad.toml", "not = [valid")

	code, _, _ := runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 2, code)
}

func TestHostCommands_RefuseAHostThatWillNotOpen(t *testing.T) {
	t.Parallel()

	info, env, dir := hostRig(t, &fakeMachine{})
	info.Host.Open = func(model.Project) (cli.Machine, error) { return nil, errors.New("no [host] table") }

	code, _, stderr := runCLI(t, env, info, "host", "clean", "p", "--config", dir)

	require.Equal(t, 2, code)
	assert.Contains(t, stderr, "no [host] table")
}

func TestHostCommands_RefuseAConfigFolderThatNeedsAHomeFolder(t *testing.T) {
	t.Setenv("HOME", "")

	info, env, _ := hostRig(t, &fakeMachine{})

	code, _, _ := runCLI(t, env, info, "host", "clean", "p", "--config", "~/obee")

	require.Equal(t, 2, code)
}

func TestHostCommands_AreLeftOutWhenNoHostOpens(t *testing.T) {
	t.Parallel()

	code, _, _ := runCLI(t, nil, cli.BuildInfo{}, "host", "clean", "p")

	require.NotEqual(t, 0, code)
}
