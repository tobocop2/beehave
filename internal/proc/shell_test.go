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

	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc"
)

func TestRunShell_ReturnsTheExitCodeOfTheCommand(t *testing.T) {
	t.Parallel()

	for _, code := range []string{"0", "1", "2", "7"} {
		t.Run("exit_"+code, func(t *testing.T) {
			t.Parallel()

			_, got := proc.RunShell(context.Background(), t.TempDir(), "exit "+code)
			assert.Equal(t, map[string]int{"0": 0, "1": 1, "2": 2, "7": 7}[code], got)
		})
	}
}

func TestRunShell_ACommandKilledBySignalReadsAs128PlusTheSignal(t *testing.T) {
	t.Parallel()

	_, code := proc.RunShell(context.Background(), t.TempDir(), "kill -TERM $$")
	assert.Equal(t, 128+15, code)
}

func TestRunShell_RunsInTheFolderAndKeepsBothStreamsInOrder(t *testing.T) {
	t.Parallel()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	output, code := proc.RunShell(context.Background(), dir, "pwd; echo out-line; echo err-line >&2; echo last")
	assert.Zero(t, code)
	assert.Equal(t, dir+"\nout-line\nerr-line\nlast\n", output)
}

func TestRunShell_AFolderThatDoesNotExistReadsAs127WithTheReason(t *testing.T) {
	t.Parallel()

	output, code := proc.RunShell(context.Background(), filepath.Join(t.TempDir(), "gone"), "true")
	assert.Equal(t, 127, code)
	assert.Contains(t, output, "no such file or directory")
}

func TestRunShell_ACancelKillsTheCommandAndItsChildren(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pidFile := filepath.Join(t.TempDir(), "child")
	codes := make(chan int, 1)

	go func() {
		_, code := proc.RunShell(ctx, t.TempDir(), "sleep 30 & echo $! > "+pidFile+"; sleep 30")
		codes <- code
	}()

	// The cancel waits until the file holds the pid, so the shell has started the child before the group dies.
	var child int

	require.Eventually(t, func() bool {
		text, err := os.ReadFile(pidFile) //nolint:gosec // the test names its own file
		if err != nil {
			return false
		}

		child, err = strconv.Atoi(strings.TrimSpace(string(text)))

		return err == nil
	}, 20*time.Second, 5*time.Millisecond, "the shell did not write the pid of the child")

	cancel()

	assert.Equal(t, 128+9, <-codes)
	// The background sleep dies with the group. The wait is for the signal to land, and it ends at once when the child is gone.
	assert.Eventually(t, func() bool { return errors.Is(syscall.Kill(child, 0), syscall.ESRCH) }, 20*time.Second, 10*time.Millisecond, "the background sleep is still alive")
}

func TestRunShell_KeepsTheLastBytesOfAnOutputPastTheCap(t *testing.T) {
	t.Parallel()

	output, code := proc.RunShell(context.Background(), t.TempDir(), "head -c 3000000 /dev/zero | tr '\\0' x; echo; echo THE-END")
	assert.Zero(t, code)
	assert.LessOrEqual(t, len(output), model.ReportLimit)
	assert.Greater(t, len(output), model.ReportLimit-16)
	assert.True(t, strings.HasSuffix(output, "xxx\nTHE-END\n"), output[len(output)-20:])
}

func TestRunShell_AnOutputUnderTheCapIsKeptWhole(t *testing.T) {
	t.Parallel()

	output, _ := proc.RunShell(context.Background(), t.TempDir(), "head -c 10000 /dev/zero | tr '\\0' y")
	assert.Len(t, output, 10000)
}
