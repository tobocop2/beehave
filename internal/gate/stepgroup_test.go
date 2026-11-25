package gate_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
	"github.com/tobocop2/beehave/internal/model"
)

// childOnlyTimeout models a `timeout` that makes itself a group leader and sends TERM and then KILL to its child only, never to the group.
// The perl line is the setpgid(0, 0) call that a shell script cannot make.
const childOnlyTimeout = `#!/bin/sh
if [ -z "$FAKE_TIMEOUT_LEADER" ]; then
	FAKE_TIMEOUT_LEADER=1 exec perl -e 'setpgrp(0, 0); exec @ARGV' "$0" "$@"
fi
grace=0
case "$1" in
--kill-after=*) grace=${1#--kill-after=}; shift ;;
esac
limit=$1
shift
"$@" &
c=$!
( sleep "$limit"; kill -s TERM "$c" 2>/dev/null; if [ "$grace" -gt 0 ]; then sleep "$grace"; kill -s KILL "$c" 2>/dev/null; fi ) &
w=$!
wait "$c"
rc=$?
kill "$w" 2>/dev/null
exit "$rc"
`

// sleepersOf lists the pids of the processes whose command line is exactly `sleep <arg>`.
func sleepersOf(t *testing.T, arg string) []int {
	t.Helper()

	out, err := exec.CommandContext(context.Background(), "ps", "-axo", "pid=,command=").Output()
	require.NoError(t, err)

	var pids []int

	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[1] == "sleep" && fields[2] == arg {
			pid, convErr := strconv.Atoi(fields[0])
			require.NoError(t, convErr)

			pids = append(pids, pid)
		}
	}

	return pids
}

// killSleepers stops each process of `sleep <arg>` when the test ends, so a failed test leaves no sleeper behind.
func killSleepers(t *testing.T, arg string) {
	t.Helper()

	t.Cleanup(func() {
		for _, pid := range sleepersOf(t, arg) {
			if proc, err := os.FindProcess(pid); err == nil {
				if killErr := proc.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
					t.Errorf("stop sleeper %d: %v", pid, killErr)
				}
			}
		}
	})
}

// requireCounterSeesASleeper is the positive control: the counter finds a sleeper that the test starts itself.
func requireCounterSeesASleeper(t *testing.T, arg string) {
	t.Helper()

	killSleepers(t, arg)

	sleeper := exec.CommandContext(t.Context(), "sleep", arg) //nolint:gosec // arg is a constant of the calling test, and the test context stops the sleeper
	require.NoError(t, sleeper.Start())

	require.Eventually(t, func() bool { return len(sleepersOf(t, arg)) == 1 }, 5*time.Second, 50*time.Millisecond, "the counter must see a sleeper that runs")
}

// timedStepProject is a host gate with a step that has a one second timeout, then a step that records that it ran.
func timedStepProject(step string) model.Project {
	project := hostProject()
	project.Gate = ""
	project.GateSteps = []model.GateSpec{{Name: "one", Command: step, Timeout: time.Second}, {Name: "two", Command: `touch "$HOME/gate-ran"`}}

	return project
}

// childOnlyTimeoutDir is a folder with the model timeout under the name `timeout`.
func childOnlyTimeoutDir(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("no perl on this machine")
	}

	dir := t.TempDir()
	require.NoError(t, exectest.Write(filepath.Join(dir, "timeout"), childOnlyTimeout))

	return dir
}

func TestHostCommand_AKilledStepLeavesNoChildAliveWhenTimeoutOnlyKillsTheChild(t *testing.T) {
	t.Parallel()

	const arg = "4301.01"

	requireCounterSeesASleeper(t, "4398.01")
	killSleepers(t, arg)

	code, _, ran, out := runProjectCommandOn(t, timedStepProject("trap '' TERM; sleep "+arg), []string{childOnlyTimeoutDir(t)}, "SHIM_HEAD="+headA)

	assert.Equal(t, 1, code, "the failed step fails the job")
	assert.True(t, ran, "the later step still runs")
	assert.Contains(t, out, "beehave-gate-step one rc=137\n")
	assert.Empty(t, sleepersOf(t, arg), "the child of the killed step is gone")
}

func TestHostCommand_AKilledStepLeavesNoGrandchildAliveWhenTimeoutOnlyKillsTheChild(t *testing.T) {
	t.Parallel()

	const grandchild, child = "4302.01", "4303.01"

	requireCounterSeesASleeper(t, "4399.01")
	killSleepers(t, grandchild)
	killSleepers(t, child)

	code, _, ran, out := runProjectCommandOn(t, timedStepProject("trap '' TERM; ("+"sleep "+grandchild+" &); sleep "+child), []string{childOnlyTimeoutDir(t)}, "SHIM_HEAD="+headA)

	assert.Equal(t, 1, code, "the failed step fails the job")
	assert.True(t, ran, "the later step still runs")
	assert.Contains(t, out, "beehave-gate-step one rc=137\n")
	assert.Empty(t, sleepersOf(t, grandchild), "the grandchild of the killed step is gone")
	assert.Empty(t, sleepersOf(t, child), "the child of the killed step is gone")
}

func TestHostCommand_AStepThatEndsInTimeKeepsItsBackgroundChild(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("timeout"); err != nil {
		t.Skip("no timeout binary on this machine")
	}

	const arg = "4304.01"

	killSleepers(t, arg)

	code, _, ran, out := runProjectCommandOn(t, timedStepProject("sleep "+arg+" & echo started"), nil, "SHIM_HEAD="+headA)

	assert.Equal(t, 0, code)
	assert.True(t, ran)
	assert.Contains(t, out, "beehave-gate-step one rc=0\n")
	assert.Len(t, sleepersOf(t, arg), 1, "a step that ends in time is not killed, so its background child stays")
}
