package gate_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/exectest"
	"github.com/tobocop2/beehest/internal/gate"
	"github.com/tobocop2/beehest/internal/model"
)

// sweepMachine is a fake host: a work root, and the shim git and the tools that the job command needs on its PATH.
type sweepMachine struct {
	t    *testing.T
	home string
	env  []string
}

func newSweepMachine(t *testing.T) sweepMachine {
	t.Helper()

	home, bin := t.TempDir(), t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), gitShim))

	dirs := []string{bin, "/usr/bin", "/bin"}

	for _, tool := range []string{"timeout", "flock"} {
		found, err := exec.LookPath(tool)
		require.NoError(t, err, "the job needs %s on this machine", tool)

		dirs = append(dirs, filepath.Dir(found))
	}

	return sweepMachine{t: t, home: home, env: []string{"HOME=" + home, "PATH=" + strings.Join(dirs, ":"), "SHIM_HEAD=" + headA}}
}

// command is the job command that the gate sends to the host for a project of this name, with the gate command given.
func (m sweepMachine) command(name, gateCommand string) string {
	m.t.Helper()

	project := model.Project{Name: name, Repo: "owner/name", Gate: gateCommand, BuildHost: "gpu-a"}
	remote := &fakeRemote{state: model.JobSucceeded, root: m.home}
	_, err := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote).Run(context.Background(), hostRequest(project))
	require.NoError(m.t, err)
	require.Len(m.t, remote.submitted, 1)

	return remote.submitted[0].Command
}

// start runs a job in its own process group, as a systemd unit has its own cgroup, and returns once the job holds its clone folder.
func (m sweepMachine) start(name string) (stop func()) {
	m.t.Helper()

	cmd := exec.CommandContext(m.t.Context(), "/bin/sh", "-c", m.command(name, `touch "$HOME/`+name+`-running"; sleep 60; true`)) //nolint:gosec // the command is the one the code under test composes
	cmd.Dir, cmd.Env = m.home, m.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(m.t, cmd.Start())

	stop = func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) //nolint:errcheck,gosec // the job may be dead already
		cmd.Wait()                                      //nolint:errcheck,gosec // a killed job has no exit status to read

		// The kernel frees the lock when the last process of the group is gone, which can be after the shell is reaped.
		assert.Eventually(m.t, func() bool { return errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.ESRCH) }, 20*time.Second, 10*time.Millisecond, "the job group is gone")
	}

	m.t.Cleanup(stop)
	require.Eventually(m.t, func() bool { return exists(filepath.Join(m.home, name+"-running")) }, 20*time.Second, 10*time.Millisecond, "the job %s did not start", name)

	return stop
}

// runToEnd runs a job that passes at once, and returns its exit code.
func (m sweepMachine) runToEnd(name string) int {
	m.t.Helper()

	cmd := exec.CommandContext(m.t.Context(), "/bin/sh", "-c", m.command(name, "true")) //nolint:gosec // the command is the one the code under test composes
	cmd.Dir, cmd.Env = m.home, m.env

	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok { //nolint:errorlint // the error of Run is the exit error itself
		return exit.ExitCode()
	}

	require.NoError(m.t, err)

	return 0
}

func (m sweepMachine) clone(name string) string {
	return filepath.Join(m.home, "beehest-gate-"+name+"-"+headA)
}

func exists(path string) bool {
	_, err := os.Lstat(path)

	return err == nil
}

func TestHostCommand_AJobKilledBySIGKILLLeavesItsCloneAndTheNextJobRemovesIt(t *testing.T) {
	t.Parallel()

	m := newSweepMachine(t)
	stop := m.start("dead")
	require.DirExists(t, m.clone("dead"), "the job holds its clone folder")

	stop()
	require.DirExists(t, m.clone("dead"), "SIGKILL runs no trap, so the clone stays")

	assert.Equal(t, 0, m.runToEnd("next"))
	assert.NoDirExists(t, m.clone("dead"), "the next job removed the clone of the dead job")
	assert.NoDirExists(t, m.clone("next"), "the next job removed its own clone")
}

func TestHostCommand_ALiveJobKeepsItsCloneWhenAnotherJobStarts(t *testing.T) {
	t.Parallel()

	m := newSweepMachine(t)
	stop := m.start("live")

	assert.Equal(t, 0, m.runToEnd("next"))
	assert.DirExists(t, m.clone("live"), "the sweep leaves the clone of a job that lives")
	assert.FileExists(t, filepath.Join(m.clone("live"), ".beehest-job"))

	stop()
}

func TestHostCommand_TheSweepKeepsAFolderWhoseJobCannotBeRead(t *testing.T) {
	t.Parallel()

	m := newSweepMachine(t)

	noMarker := filepath.Join(m.home, "beehest-gate-nomarker-"+headA)
	require.NoError(t, os.Mkdir(noMarker, 0o750))

	target := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.Mkdir(target, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(target, ".beehest-job"), nil, 0o600))

	link := filepath.Join(m.home, "beehest-gate-link-"+headA)
	require.NoError(t, os.Symlink(target, link))

	// The record of this job is a link to nothing, so the sweep cannot read it.
	locked := filepath.Join(m.home, "beehest-gate-locked-"+headA)
	require.NoError(t, os.Mkdir(locked, 0o750))
	require.NoError(t, os.Symlink(filepath.Join(target, "missing"), filepath.Join(locked, ".beehest-job")))

	dead := filepath.Join(m.home, "beehest-gate-dead-"+headA)
	require.NoError(t, os.Mkdir(dead, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dead, ".beehest-job"), nil, 0o600))

	assert.Equal(t, 0, m.runToEnd("next"))

	assert.NoDirExists(t, dead, "the control: a folder with the marker and a free lock is removed")
	assert.DirExists(t, noMarker, "a folder with no marker stays")
	assert.FileExists(t, filepath.Join(target, ".beehest-job"), "the target of a link stays")
	assert.True(t, exists(link), "a link stays")
	assert.True(t, exists(locked), "a folder that cannot be read stays")
}
