package gate_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
)

const hostCommand = `dir=$(mktemp -d "$HOME/obee-gate.XXXXXX") && trap 'rm -rf "$dir"' EXIT && cd "$dir" && git clone -- https://github.com/owner/name.git tree && cd tree && ` +
	`git checkout --detach a1b2c3 && [ "$(git rev-parse HEAD)" = a1b2c3 ] && sh -c 'make check'`

func hostProject() model.Project {
	return model.Project{Name: "demo", Repo: "owner/name", Gate: "make check", BuildHost: "gpu-a"}
}

func TestRun_ThePathToAHostGateChecksOutTheSentCommitInAFreshFolderBeforeTheGate(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(hostProject()))
	require.NoError(t, err)
	assert.Equal(t, []model.JobSpec{{Name: "gate-demo", Command: hostCommand}}, remote.submitted)
}

func TestRun_EveryWordOfTheHostCommandIsQuoted(t *testing.T) {
	t.Parallel()

	project := hostProject()
	project.Gate = "make 'check'; rm x"
	remote := &fakeRemote{state: model.JobSucceeded}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean("a b")}}, remote)

	_, err := g.Run(context.Background(), hostRequest(project))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)
	assert.Contains(t, remote.submitted[0].Command, `git checkout --detach 'a b' && [ "$(git rev-parse HEAD)" = 'a b' ] && sh -c 'make '\''check'\''; rm x'`)
}

// gitShim is a git that the test steers by environment: the clone makes the folder, the checkout and the head come from the variables.
const gitShim = `#!/bin/sh
case "$1" in
clone) [ -z "$SHIM_CLONE_FAILS" ] || exit 128; mkdir "$4" ;;
checkout) [ -z "$SHIM_CHECKOUT_FAILS" ] || exit 1 ;;
rev-parse) echo "$SHIM_HEAD" ;;
esac
`

// runGateCommand runs the command that the gate sends to the host for a gate command, with the shim as git.
// It returns the exit code, the login folder that the command ran under, and whether the gate command ran.
func runGateCommand(t *testing.T, gateCommand string, env ...string) (int, string, bool) {
	t.Helper()

	home, bin := t.TempDir(), t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(gitShim), 0o700)) //nolint:gosec // a fake program must be executable

	remote := &fakeRemote{state: model.JobSucceeded}
	project := hostProject()
	project.Gate = gateCommand
	_, err := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote).Run(context.Background(), hostRequest(project))
	require.NoError(t, err)
	require.Len(t, remote.submitted, 1)

	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", remote.submitted[0].Command) //nolint:gosec // the command is the one the code under test composes, run against a shim git
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}, env...)

	code := 0

	var exit *exec.ExitError
	if err := cmd.Run(); errors.As(err, &exit) {
		code = exit.ExitCode()
	} else {
		require.NoError(t, err)
	}

	_, statErr := os.Stat(filepath.Join(home, "gate-ran"))

	return code, home, statErr == nil
}

// runHostCommand runs the command that the gate sends to the host and reports whether it exited 0 and whether the gate command ran.
func runHostCommand(t *testing.T, env ...string) (bool, bool) {
	t.Helper()

	code, _, ran := runGateCommand(t, `touch "$HOME/gate-ran"`, env...)

	return code == 0, ran
}

func TestHostCommand_TheCloneFolderIsRemovedWhenTheShellExitsAndAFailingGateKeepsItsStatus(t *testing.T) {
	t.Parallel()

	for gateCommand, want := range map[string]int{"exit 0": 0, "exit 3": 3} {
		code, home, _ := runGateCommand(t, gateCommand, "SHIM_HEAD="+headA)
		assert.Equal(t, want, code, gateCommand)

		left, err := filepath.Glob(filepath.Join(home, "obee-gate.*"))
		require.NoError(t, err)
		assert.Empty(t, left, gateCommand)
	}
}

func TestHostCommand_AFailedCloneAlsoRemovesTheFolder(t *testing.T) {
	t.Parallel()

	code, home, _ := runGateCommand(t, "exit 0", "SHIM_HEAD="+headA, "SHIM_CLONE_FAILS=1")
	assert.NotZero(t, code)

	left, err := filepath.Glob(filepath.Join(home, "obee-gate.*"))
	require.NoError(t, err)
	assert.Empty(t, left)
}

func TestHostCommand_PassesWhenTheCheckedOutHeadIsTheSentCommit(t *testing.T) {
	t.Parallel()

	passed, ran := runHostCommand(t, "SHIM_HEAD="+headA)
	assert.True(t, passed)
	assert.True(t, ran)
}

func TestHostCommand_ACloneThatFailsIsAFailedGateAndTheGateDoesNotRun(t *testing.T) {
	t.Parallel()

	passed, ran := runHostCommand(t, "SHIM_HEAD="+headA, "SHIM_CLONE_FAILS=1")
	assert.False(t, passed)
	assert.False(t, ran)
}

func TestHostCommand_ACommitThatIsNotPushedFailsTheCheckoutAndTheGateDoesNotRun(t *testing.T) {
	t.Parallel()

	passed, ran := runHostCommand(t, "SHIM_HEAD="+headA, "SHIM_CHECKOUT_FAILS=1")
	assert.False(t, passed)
	assert.False(t, ran)
}

func TestHostCommand_AHeadThatIsNotTheSentCommitFailsAndTheGateDoesNotRun(t *testing.T) {
	t.Parallel()

	passed, ran := runHostCommand(t, "SHIM_HEAD=ffffff")
	assert.False(t, passed)
	assert.False(t, ran)
}
