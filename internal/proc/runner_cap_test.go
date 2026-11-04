package proc_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/proc"
)

// forkScript starts 40 sleeps of 2 seconds in the background. A start that the kernel refuses ends the shell with a non-zero code.
// The loop ends by itself, and the exit trap kills every sleep it started.
const forkScript = `pids=
trap 'kill $pids 2>/dev/null' EXIT
i=0
while [ $i -lt 40 ]; do
  sleep 2 &
  pids="$pids $!"
  i=$((i + 1))
done
echo started $i`

// The tests below run on every system. On macOS the cap counts every process of the user, so they run one at a time.

func TestExecRunner_AProcessCapStopsAStartPastIt(t *testing.T) { //nolint:paralleltest // on macOS the cap counts every process of the user, so a fork test runs alone
	requireCap(t)

	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 3}.Run(context.Background(), []string{"-c", forkScript})

	assert.NotZero(t, done.ExitCode, "a start past the cap fails the shell")
	assert.NotContains(t, done.Stdout, "started 40")
}

func TestExecRunner_NoProcessCapStartsEveryProcess(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second}.Run(context.Background(), []string{"-c", forkScript})

	assert.Zero(t, done.ExitCode, done.Stderr)
	assert.Contains(t, done.Stdout, "started 40")
}

func TestExecRunner_AProcessCapKeepsTheArgumentsAndTheExitCodeOfTheCommand(t *testing.T) { //nolint:paralleltest // see above
	requireCap(t)

	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), []string{"-c", `printf %s "$0 $1"; exit 4`, "a", "b c"})

	assert.Equal(t, 4, done.ExitCode)
	assert.Equal(t, "a b c", done.Stdout)
}

func TestExecRunner_AProcessCapOnAMissingCommandReadsAsNotFound(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "beehave-no-such-binary", MaxNewProcesses: 10}.Run(context.Background(), nil)

	assert.Equal(t, 127, done.ExitCode)
	assert.Contains(t, done.Stderr, "command not found")
}

func TestExecRunner_ACapDoesNotReadTheRequestPath(t *testing.T) { //nolint:paralleltest // see above
	requireCap(t)

	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, Only: []string{"PATH=/nonexistent"}, MaxNewProcesses: 50}.Run(context.Background(), []string{"-c", "echo ok"})

	assert.Zero(t, done.ExitCode, done.Stderr)
	assert.Equal(t, "ok\n", done.Stdout)
	assert.Empty(t, done.Stderr, "no tool is looked up in the PATH of the request")
}

func TestExecRunner_APathThatCannotBeMadeAbsoluteFailsTheCallAndTheCommandDoesNotRun(t *testing.T) { //nolint:paralleltest // see above
	marker := filepath.Join(t.TempDir(), "ran")
	abs := func(string) (string, error) { return "", errors.New("no working folder") }
	done := proc.WithAbs(proc.ExecRunner{Binary: "./tool.sh", Dir: "rel", MaxNewProcesses: 50}, abs).Run(context.Background(), []string{marker})

	assert.Equal(t, proc.ExitCapFailed, done.ExitCode)
	require.EqualError(t, done.SetupErr, "could not resolve ./tool.sh in rel: no working folder")
	assert.NoFileExists(t, marker)
}

func TestExecRunner_ACappedProgramPathThatIsMissingReadsAsNotFound(t *testing.T) { //nolint:paralleltest // see above
	done := proc.ExecRunner{Binary: "./absent.sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), nil)

	require.NoError(t, done.SetupErr)
	assert.Equal(t, 127, done.ExitCode)
	assert.NotEmpty(t, done.Stderr)
}

func TestExecRunner_AProgramThatExits125WithItsOwnCouldNotTextKeepsItsResult(t *testing.T) { //nolint:paralleltest // see above
	requireCap(t)

	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: 30 * time.Second, MaxNewProcesses: 1000}.Run(context.Background(), []string{"-c", `echo out; echo "could not open the thing" >&2; exit 125`})

	require.NoError(t, done.SetupErr)
	assert.Equal(t, 125, done.ExitCode)
	assert.Equal(t, "out\n", done.Stdout)
	assert.Equal(t, "could not open the thing\n", done.Stderr)
}

func TestExecRunner_ACappedCallThatCannotStartReadsAsNotFound(t *testing.T) { //nolint:paralleltest // see above
	requireCap(t)

	done := proc.ExecRunner{Binary: "sh", Dir: filepath.Join(t.TempDir(), "missing"), MaxNewProcesses: 1000}.Run(context.Background(), nil)

	require.NoError(t, done.SetupErr)
	assert.Equal(t, 127, done.ExitCode)
	assert.NotEmpty(t, done.Stderr)
}

func TestExecRunner_ACappedCommandGetsExactlyTheEnvironmentItIsGiven(t *testing.T) { //nolint:paralleltest // see above
	requireCap(t)

	runner := proc.ExecRunner{Binary: "env", Timeout: 30 * time.Second, Only: []string{"BEEHAVE_ONLY=one"}, Env: []string{"BEEHAVE_PROC_TEST=seen"}}
	plain := runner.Run(context.Background(), nil)
	runner.MaxNewProcesses = 1000
	capped := runner.Run(context.Background(), nil)

	require.NoError(t, capped.SetupErr)
	require.Zero(t, plain.ExitCode, plain.Stderr)
	assert.Equal(t, "BEEHAVE_ONLY=one\nBEEHAVE_PROC_TEST=seen\n", capped.Stdout, "no variable of beehave reaches the command, also none that the cap itself needs")
	assert.Equal(t, plain.Stdout, capped.Stdout)
}
