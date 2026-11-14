//go:build !linux

package proc_test

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
	"golang.org/x/sys/unix"

	"github.com/tobocop2/beehave/internal/proc"
)

func TestExecRunner_AProcessCapSetsTheLimitWhateverShellIsBinSh(t *testing.T) { //nolint:paralleltest // see above
	if _, err := os.Stat("/proc/self/limits"); err != nil {
		t.Skip("the limits of a process are read from /proc")
	}

	count := func() (int, error) { return 2000, nil }
	done := proc.WithCount(proc.ExecRunner{Binary: "cat", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}, count).Run(context.Background(), []string{"/proc/self/limits"})

	require.NoError(t, done.SetupErr)
	require.Zero(t, done.ExitCode, done.Stderr)
	assert.Regexp(t, `(?m)^Max processes\s+3000\s+3000\s`, done.Stdout)
}

// capHelperRun runs the cap helper with fake system calls and returns what it wrote on its error file, its exit code (-1 for none), the limit it set and the program it ran.
func capHelperRun(t *testing.T, argv []string, setErr, execErr error) (written string, code int, limit uint64, program string) {
	t.Helper()

	fail, err := os.Create(filepath.Join(t.TempDir(), "fd3"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = fail.Close() })

	code = -1
	require.NoError(t, proc.RunCapHelper(argv, fail,
		func(_ int, rlim *unix.Rlimit) error { limit = rlim.Cur; return setErr },
		func(path string, _, _ []string) error { program = path; return execErr },
		func(c int) { code = c }))

	data, err := os.ReadFile(fail.Name())
	require.NoError(t, err)

	return string(data), code, limit, program
}

func TestExecRunner_AProgramThatCannotBeExecutedFailsTheStepWithTheCause(t *testing.T) { //nolint:paralleltest // see above
	program := filepath.Join(t.TempDir(), "noformat")
	require.NoError(t, os.WriteFile(program, []byte("not a program\n"), 0o700)) //nolint:gosec // the test needs an executable file

	done := proc.ExecRunner{Binary: program, Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), nil)

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.Error(t, done.SetupErr)
	assert.Contains(t, done.SetupErr.Error(), "could not set max_new_processes = ")
	assert.Contains(t, done.SetupErr.Error(), program+": exec format error")
}

func TestExecRunner_ACapWithoutTheRunningProgramFailsTheStepAndTheCommandDoesNotRun(t *testing.T) { //nolint:paralleltest // see above
	marker := filepath.Join(t.TempDir(), "ran")
	executable := func() (string, error) { return "", errors.New("no executable") }
	done := proc.WithExecutable(proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), MaxNewProcesses: 50}, executable).Run(context.Background(), []string{"-c", "touch " + marker})

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.ErrorContains(t, done.SetupErr, "could not find the running program")
	assert.NoFileExists(t, marker)
}

func TestExecRunner_ACapWithAProgramThatCannotStartFailsTheStepNamingThePath(t *testing.T) { //nolint:paralleltest // see above
	gone := filepath.Join(t.TempDir(), "beehave-gone")
	runner := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), MaxNewProcesses: 50}
	done := proc.WithExecutable(runner, func() (string, error) { return gone, nil }).Run(context.Background(), []string{"-c", "exit 0"})

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.ErrorContains(t, done.SetupErr, "could not set max_new_processes")
	require.ErrorContains(t, done.SetupErr, gone)
}

func TestExecRunner_ACapWithoutASetupPipeFailsTheStepAndTheCommandDoesNotRun(t *testing.T) { //nolint:paralleltest // see above
	marker := filepath.Join(t.TempDir(), "ran")
	pipe := func() (*os.File, *os.File, error) { return nil, nil, errors.New("no descriptors left") }
	done := proc.WithPipe(proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), MaxNewProcesses: 50}, pipe).Run(context.Background(), []string{"-c", "touch " + marker})

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.ErrorContains(t, done.SetupErr, "could not make the setup pipe of the cap")
	require.ErrorContains(t, done.SetupErr, "no descriptors left")
	assert.NoFileExists(t, marker)
}

func TestCapHelper_AnotherProgramNameDoesNothing(t *testing.T) {
	t.Parallel()

	written, code, _, _ := capHelperRun(t, []string{"beehave", "5", "/bin/true"}, nil, nil)
	assert.Empty(t, written)
	assert.Equal(t, -1, code)

	written, code, _, _ = capHelperRun(t, nil, nil, nil)
	assert.Empty(t, written)
	assert.Equal(t, -1, code)
}

func TestCapHelper_SetsTheLimitThenRunsTheProgram(t *testing.T) {
	t.Parallel()

	written, code, limit, program := capHelperRun(t, []string{proc.CapArg0, "42", "/bin/true", "x"}, nil, errors.New("exec returned"))

	assert.Equal(t, uint64(42), limit)
	assert.Equal(t, "/bin/true", program)
	assert.Equal(t, proc.ExitCapFailed, code, "an exec that returns is a failure")
	assert.Contains(t, written, "/bin/true: exec returned")
}

func TestCapHelper_AFailureIsWrittenAndTheProgramDoesNotRun(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		argv   []string
		setErr error
		want   string
	}{
		"no program":      {[]string{proc.CapArg0, "5"}, nil, "needs a limit and a program"},
		"not a number":    {[]string{proc.CapArg0, "many", "/bin/true"}, nil, "invalid syntax"},
		"limit not taken": {[]string{proc.CapArg0, "5", "/bin/true"}, syscall.EPERM, "setrlimit: operation not permitted"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			written, code, _, program := capHelperRun(t, test.argv, test.setErr, errors.New("must not run"))

			assert.Contains(t, written, test.want)
			assert.Equal(t, proc.ExitCapFailed, code)
			assert.Empty(t, program)
		})
	}
}

func TestExecRunner_AFailedCountFailsTheStepAndTheCommandDoesNotRun(t *testing.T) { //nolint:paralleltest // see above
	marker := filepath.Join(t.TempDir(), "ran")
	count := func() (int, error) { return 0, errors.New("no table") }
	done := proc.WithCount(proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), MaxNewProcesses: 50}, count).Run(context.Background(), []string{"-c", "touch " + marker})

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.EqualError(t, done.SetupErr, "could not count the user's processes: no table")
	assert.NoFileExists(t, marker)
}

func TestExecRunner_ALimitThatCannotBeSetFailsTheStepAndTheCommandDoesNotRun(t *testing.T) { //nolint:paralleltest // see above
	hard := proc.ExecRunner{Binary: "sh", Dir: t.TempDir()}.Run(context.Background(), []string{"-c", "ulimit -H -u"})
	if limit, err := strconv.Atoi(strings.TrimSpace(hard.Stdout)); err != nil || limit > 100000 {
		t.Skip("the hard limit of processes is too high to test a limit that cannot be set")
	}

	marker := filepath.Join(t.TempDir(), "ran")
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 100000}.Run(context.Background(), []string{"-c", "touch " + marker})

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.Error(t, done.SetupErr)
	assert.True(t, strings.HasPrefix(done.SetupErr.Error(), "could not set max_new_processes = 100000: "), done.SetupErr.Error())
	assert.NoFileExists(t, marker)
}

func TestExecRunner_ACapOnAProgramPathRelativeToDirIsApplied(t *testing.T) { //nolint:paralleltest // see above
	hard := proc.ExecRunner{Binary: "sh", Dir: t.TempDir()}.Run(context.Background(), []string{"-c", "ulimit -H -u"})
	if limit, err := strconv.Atoi(strings.TrimSpace(hard.Stdout)); err != nil || limit > 100000 {
		t.Skip("the hard limit of processes is too high to test a limit that cannot be set")
	}

	dir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "probe.sh"), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)) //nolint:gosec // the test script must be executable

	done := proc.ExecRunner{Binary: "./probe.sh", Dir: dir, Timeout: 30 * time.Second, MaxNewProcesses: 100000}.Run(context.Background(), nil)

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.Error(t, done.SetupErr)
	assert.True(t, strings.HasPrefix(done.SetupErr.Error(), "could not set max_new_processes = 100000: "), done.SetupErr.Error())
	assert.NoFileExists(t, marker)
}

// requireCap does nothing here: the per-user limit needs no scope.
func requireCap(*testing.T) {}

func TestExecRunner_AProgramThatPrintsTheOldSentinelKeepsItsResult(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), []string{"-c", `echo beehave-cap-not-set >&2; echo out; exit 125`})

	require.NoError(t, done.SetupErr)
	assert.Equal(t, 125, done.ExitCode)
	assert.Equal(t, "out\n", done.Stdout)
	assert.Equal(t, "beehave-cap-not-set\n", done.Stderr)
}

func TestExecRunner_AProgramCannotWriteToTheSetupPipe(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), []string{"-c", `echo forged >&3 2>/dev/null; echo out`})

	require.NoError(t, done.SetupErr)
	assert.Equal(t, "out\n", done.Stdout)
}
