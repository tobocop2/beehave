package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/gitexec"
)

func TestDisposableTree_DoesNotRunThePostCheckoutHookOfTheRepository(t *testing.T) {
	t.Parallel()

	dir := repoWithCommit(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o700))
	require.NoError(t, exectest.Write(hook, "#!/bin/sh\ntouch '"+marker+"'\n"))

	copied, release, err := disposableTree(t.Context(), dir, t.TempDir(), gateCloneOf(dir, cloneLock{repos: t.TempDir()}.of("p")))
	require.NoError(t, err)

	defer release()

	assert.DirExists(t, copied.WorkTree)
	assert.NoFileExists(t, marker, "the worktree add ran the hook of the repository")
}

func putGitOnPath(t *testing.T, script string) {
	t.Helper()

	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
}

func TestGitIn_AFailureKeepsTheTextOfGit(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	putGitOnPath(t, "#!/bin/sh\necho 'fatal: not a repository' >&2\nexit 128\n")

	_, err := gitIn(t.Context(), gitexec.New(t.TempDir(), 0), "status")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 128")
	assert.Contains(t, err.Error(), "fatal: not a repository")
}

func TestGitIn_ATimeoutReadsAsATimeout(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	putGitOnPath(t, "#!/bin/sh\nsleep 5\n")

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_, err := gitIn(ctx, gitexec.New(t.TempDir(), 0), "status")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.NotContains(t, err.Error(), "canceled")
}

func TestGitIn_ACancelReadsAsACancel(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	putGitOnPath(t, "#!/bin/sh\nsleep 5\n")

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(200*time.Millisecond, cancel)

	_, err := gitIn(ctx, gitexec.New(t.TempDir(), 0), "status")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "was canceled")
}

func TestEveryPathToGitRunsWithTheHooksOff(t *testing.T) { //nolint:paralleltest // t.Setenv changes PATH for the process
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	putGitOnPath(t, "#!/bin/sh\necho \"$@\" >> "+log+"\n")

	// The folder is a clone in the engine layout, so each path pins its repository. The read of the clone settings comes first and runs no hook.
	state := t.TempDir()
	repos := filepath.Join(state, "repos")
	dir := filepath.Join(repos, "p")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o700))

	paths := map[string]func() model.CommandResult{
		"gitIn": func() model.CommandResult {
			out, err := gitIn(t.Context(), gitexec.NewRepositories(repos).Runner(dir, 0), "status")

			return model.CommandResult{Stdout: out, SetupErr: err}
		},
		"the item worktrees": func() model.CommandResult {
			return newWorktrees(nil, flow.Options{StateDir: state}, nil, nil).git(dir).Run(t.Context(), []string{"status"})
		},
		"the batch and gate": func() model.CommandResult {
			return newPrograms(config.Config{Projects: repositoryProject()}, nil, cloneLock{repos: repos}, localDisk{}, adapters{}, nil).local("p", "git", dir, 0).Run(t.Context(), []string{"status"})
		},
	}

	for name, run := range paths {
		require.NoError(t, os.WriteFile(log, nil, 0o600))
		assert.NoError(t, run().SetupErr, name)
		data, err := os.ReadFile(log) //nolint:gosec // a file of the test
		require.NoError(t, err, name)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		assert.True(t, strings.HasPrefix(lines[len(lines)-1], "-c core.hooksPath=/dev/null "), "%s ran git with %q", name, data)
	}
}

func TestGitFailure_AToolThatCannotStartNamesTheSetupError(t *testing.T) {
	t.Parallel()

	reason := gitFailure(t.Context(), model.CommandResult{ExitCode: proc.ExitCapFailed, SetupErr: errors.New("no count")})

	assert.Contains(t, reason, "could not start")
	assert.Contains(t, reason, "no count")
}

func TestGitIn_ReturnsWhenAChildOfGitKeepsTheOutputOpen(t *testing.T) {
	state := t.TempDir()
	pidFile := filepath.Join(state, "child.pid")
	// The child holds the output pipe of git and loops until the cleanup kills it. Git ends only after the child wrote its pid, so the child runs when git ends.
	script := "#!/bin/sh\n" +
		"sh -c 'echo $$ > \"$1\"; while :; do sleep 0.05; done' child '" + pidFile + "' &\n" +
		"while [ ! -s '" + pidFile + "' ]; do sleep 0.01; done\n" +
		"exit 0\n"
	bin := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(bin, "git"), script))
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
	// The cleanup runs before the removal of the temporary folders, kills the child, and fails the test if the child outlives it.
	t.Cleanup(func() { endChild(t, pidFile) })

	_, err := gitIn(t.Context(), gitexec.New(t.TempDir(), 0), "status")

	require.Error(t, err, "the runner reports the pipe that the child kept open")

	pid := childPid(t, pidFile)
	require.NoError(t, syscall.Kill(pid, 0), "the call waited for the child that holds the output pipe: the child is gone")
}

func childPid(t *testing.T, pidFile string) int {
	t.Helper()

	text, err := os.ReadFile(pidFile) //nolint:gosec // a file of the test
	require.NoError(t, err)

	pid, err := strconv.Atoi(strings.TrimSpace(string(text)))
	require.NoError(t, err)

	return pid
}

// endChild kills the child that wrote its pid to pidFile and waits until the process is gone.
func endChild(t *testing.T, pidFile string) {
	t.Helper()

	if _, err := os.Stat(pidFile); err != nil {
		return
	}

	pid := childPid(t, pidFile)
	// A child that already ended gives ESRCH, which is fine.
	if err := syscall.Kill(pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
		require.NoError(t, err)
	}

	assert.Eventually(t, func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }, 5*time.Second, 10*time.Millisecond, "the child of git is still alive")
}
