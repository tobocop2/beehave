//go:build linux

package proc_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/proc"
)

// requireCap skips a test on a machine where a systemd user scope with a task cap cannot start.
func requireCap(t *testing.T) {
	t.Helper()

	probe := exec.CommandContext(t.Context(), "systemd-run", "--user", "--scope", "--collect", "-p", "TasksMax=1", "--", "true")
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("no systemd user scope with a task cap here: %v: %s", err, out)
	}
}

// uniqueMark returns a word that only this test run writes, so a search of the process list finds this run alone.
func uniqueMark(t *testing.T) string {
	t.Helper()

	return fmt.Sprintf("p2a-%d", time.Now().UnixNano())
}

// markedPids returns the ids of the processes whose command line holds mark. It reads the process list with pgrep, which exits 1 when it finds nothing.
func markedPids(ctx context.Context, mark string) []int {
	out, _ := exec.CommandContext(ctx, "pgrep", "-f", mark).Output() //nolint:gosec,errcheck // the mark is made by the test; a failed pgrep reads as no process

	var pids []int

	for _, field := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(field); err == nil {
			pids = append(pids, pid)
		}
	}

	return pids
}

// stopMarked ends every process that holds mark. It is the clean-up of a test whose command may leave a process behind.
func stopMarked(t *testing.T, mark string) {
	t.Helper()

	ctx := context.WithoutCancel(t.Context())

	for _, pid := range markedPids(ctx, mark) {
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Logf("could not stop %d: %v", pid, err)
		}
	}
}

// sessionScript starts a process in a new session, which a kill of the process group does not reach, then waits. Both processes hold mark.
func sessionScript(mark string) string {
	return "setsid sh -c 'sleep 60; : " + mark + "-a' & sleep 60; : " + mark + "-b"
}

func TestExecRunner_ACancelEndsAChildThatLeftTheProcessGroup(t *testing.T) {
	t.Parallel()
	requireCap(t)

	mark := uniqueMark(t)
	t.Cleanup(func() { stopMarked(t, mark) })

	running, cancel := context.WithCancel(t.Context())

	go func() {
		deadline := time.Now().Add(20 * time.Second)
		for len(markedPids(running, mark)) < 2 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}

		cancel()
	}()

	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: time.Minute, MaxNewProcesses: 10}.Run(running, []string{"-c", sessionScript(mark)})
	assert.Equal(t, 130, done.ExitCode)

	require.Eventually(t, func() bool { return len(markedPids(t.Context(), mark)) == 0 }, 10*time.Second, 100*time.Millisecond,
		"a child in its own session is still alive after the cancel")
}

// countScript starts 12 sleeps and prints the number of tasks in the control group of the script when the shell ends.
// A shell that cannot fork ends with an error, and the exit trap still runs. A read of the file with the builtin read starts no process.
const countScript = `read line < /proc/self/cgroup
trap 'read tasks < "/sys/fs/cgroup${line#0::}/pids.current"; echo tasks=$tasks' EXIT
i=0
while [ $i -lt 12 ]; do
  sleep 3 >/dev/null 2>&1 &
  i=$((i + 1))
done`

func TestExecRunner_ACapKeepsTheTreeToTheCapPlusTheCommand(t *testing.T) {
	t.Parallel()
	requireCap(t)

	const limit = 5

	done := proc.ExecRunner{Binary: "sh", Dir: t.TempDir(), Timeout: time.Minute, MaxNewProcesses: limit}.Run(t.Context(), []string{"-c", countScript})

	assert.NotZero(t, done.ExitCode, "a start past the cap ends the shell")

	var tasks int

	_, err := fmt.Sscanf(strings.TrimSpace(done.Stdout), "tasks=%d", &tasks)
	require.NoError(t, err, done.Stdout)
	assert.LessOrEqual(t, tasks, limit+1, "the tree holds the command and at most %d more tasks", limit)
	assert.Greater(t, tasks, 1, "the script started some of its sleeps")
}
