//go:build linux

package proc_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/exectest"
	"github.com/tobocop2/obee/internal/proc"
)

func TestExecRunner_AScopeIsGoneAfterTheStep(t *testing.T) {
	t.Parallel()
	requireCap(t)

	prefix := uniqueMark(t) + "-"
	runner := proc.WithScopeTools(proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: time.Minute, MaxNewProcesses: 5}, "", "", prefix)

	done := runner.Run(t.Context(), []string{"-c", "exit 3"})
	require.NoError(t, done.SetupErr)
	assert.Equal(t, 3, done.ExitCode)

	// The manager notices the empty control group a moment after the command ends.
	require.Eventually(t, func() bool {
		out, err := exec.CommandContext(t.Context(), "systemctl", "--user", "list-units", "--all", "--no-legend", prefix+"*").CombinedOutput() //nolint:gosec // the prefix is made by the test

		return err == nil && strings.TrimSpace(string(out)) == ""
	}, 10*time.Second, 100*time.Millisecond, "a scope of this step is still loaded")
}

func TestExecRunner_ACapWithNoUserManagerFailsTheStepNamingTheFix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	broken := filepath.Join(dir, "systemd-run")
	require.NoError(t, exectest.Write(broken, "#!/bin/sh\necho 'Failed to connect to bus: No medium found' >&2\nexit 1\n"))

	marker := filepath.Join(dir, "ran")
	runner := proc.WithScopeTools(proc.ExecRunner{Binary: "sh", Dir: dir, MaxNewProcesses: 5}, broken, "", "")
	done := runner.Run(t.Context(), []string{"-c", "touch " + marker})

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.ErrorContains(t, done.SetupErr, "max_new_processes needs a systemd user manager")
	require.ErrorContains(t, done.SetupErr, "No medium found")
	assert.NoFileExists(t, marker, "a command whose cap cannot be set does not run")
}

// brokenScope returns a runner whose systemd-run fails like a host with no user bus.
func brokenScope(t *testing.T) proc.ExecRunner {
	t.Helper()

	dir := t.TempDir()
	broken := filepath.Join(dir, "systemd-run")
	require.NoError(t, exectest.Write(broken, "#!/bin/sh\necho 'Failed to connect to bus: No medium found' >&2\nexit 1\n"))

	return proc.WithScopeTools(proc.ExecRunner{Binary: "sh", Dir: dir, MaxNewProcesses: 5}, broken, "", "")
}

func TestExecRunner_AFailedScopeWithNoRuntimeDirNamesTheRuntimeDirAndNotLinger(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	done := brokenScope(t).Run(t.Context(), []string{"-c", "true"})

	require.ErrorContains(t, done.SetupErr, "XDG_RUNTIME_DIR is not set")
	require.ErrorContains(t, done.SetupErr, fmt.Sprintf("XDG_RUNTIME_DIR=/run/user/%d", os.Getuid()))
	assert.NotContains(t, done.SetupErr.Error(), "enable-linger", "linger does not fix a missing runtime directory")
}

func TestExecRunner_AFailedScopeWithARuntimeDirNamesLingerAndSaysTheDirIsSet(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/4242")

	done := brokenScope(t).Run(t.Context(), []string{"-c", "true"})

	require.ErrorContains(t, done.SetupErr, "XDG_RUNTIME_DIR is set to /run/user/4242")
	require.ErrorContains(t, done.SetupErr, "loginctl enable-linger")
}

func TestExecRunner_AScopeProbeThatTheCallEndsSaysTheCallEndedAndNotLinger(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	slow := filepath.Join(dir, "systemd-run")
	require.NoError(t, exectest.Write(slow, "#!/bin/sh\nexec sleep 5\n"))

	runner := proc.WithScopeTools(proc.ExecRunner{Binary: "sh", Dir: dir, Timeout: 300 * time.Millisecond, MaxNewProcesses: 5}, slow, "", "")
	done := runner.Run(t.Context(), []string{"-c", "true"})

	require.ErrorIs(t, done.SetupErr, context.DeadlineExceeded)
	assert.NotContains(t, done.SetupErr.Error(), "enable-linger")
}

func TestExecRunner_TheScopeCapIsTheCapPlusTheCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %s
case "$*" in *TasksMax=1\ --*) exit 0 ;; esac
while [ "$1" != "--" ]; do shift; done
shift
exec "$@"
`, log)

	fake := filepath.Join(dir, "systemd-run")
	require.NoError(t, exectest.Write(fake, script))

	runner := proc.WithScopeTools(proc.ExecRunner{Binary: "sh", Dir: dir, MaxNewProcesses: 5, Only: []string{"A=b"}}, fake, "", "p2a-fake-")
	done := runner.Run(t.Context(), []string{"-c", "echo $A"})

	require.NoError(t, done.SetupErr)
	assert.Equal(t, "b\n", done.Stdout)

	calls, err := os.ReadFile(log) //nolint:gosec // a path the test made
	require.NoError(t, err)
	assert.Contains(t, string(calls), "--user --scope --collect --quiet --unit=p2a-fake-")
	assert.Contains(t, string(calls), "-p TasksMax=6 -- /usr/bin/env -i A=b ")
}

func TestExecRunner_ACapSetsThePidsMaxOfTheScopeToTheCapPlusOne(t *testing.T) {
	t.Parallel()
	requireCap(t)

	runner := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: time.Minute, MaxNewProcesses: 4, Only: []string{}}
	done := runner.Run(t.Context(), []string{"-c", `read line < /proc/self/cgroup; read max < "/sys/fs/cgroup${line#0::}/pids.max"; echo max=$max`})

	require.NoError(t, done.SetupErr)
	assert.Equal(t, "max=5\n", done.Stdout)
}
