package host_test

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

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
)

// shell returns a real shell that runs script. The job runs to its end after the test ends its context, so it takes a context that the end of the test does not cancel.
func shell(t *testing.T, script string) *exec.Cmd {
	t.Helper()

	return exec.CommandContext(context.WithoutCancel(t.Context()), "sh", "-c", script) //nolint:gosec // the test runs the script that SlotWrap builds, under a real shell
}

func TestSlotWrap_ReturnsTheCommandAsItIsForAJobWithNoSlot(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "echo hi", host.SlotWrap(model.Slot{}, "'/x'", "'/m'", "echo hi"))
}

func TestSlotWrap_PutsTheCountTheClassAndTheCommandInTheScript(t *testing.T) {
	t.Parallel()

	script := host.SlotWrap(model.Slot{Class: "bench", Count: 4}, "/x/slots/bench", "/x/u.waiting", "echo hi")

	assert.Contains(t, script, `-le 4`)
	assert.Contains(t, script, `class bench on this host`)
	assert.Contains(t, script, `bh_d=/x/slots/bench;`)
	assert.Contains(t, script, `bh_m=/x/u.waiting;`)
	assert.Contains(t, script, "sleep 2;")
	assert.True(t, strings.HasSuffix(script, "echo hi") && len(script) > len("echo hi"), "the command ends the script")
}

func TestSlotWrap_AStopSignalWhileTheJobWaitsRemovesTheMarkerAndEndsTheJob(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock is not on PATH")
	}

	dir := t.TempDir()
	slot := model.Slot{Class: "bench", Count: 1}
	folder, marker := model.ShellQuote(filepath.Join(dir, "slots")), filepath.Join(dir, "m")

	holder := shell(t, host.SlotWrap(slot, folder, model.ShellQuote(filepath.Join(dir, "holder")), "touch "+model.ShellQuote(filepath.Join(dir, "held"))+"; until test -e "+model.ShellQuote(filepath.Join(dir, "release"))+"; do sleep 0.05; done"))
	require.NoError(t, holder.Start())
	t.Cleanup(func() {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "release"), nil, 0o600))
		require.NoError(t, holder.Wait())
	})

	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "held")); return err == nil }, 10*time.Second, 20*time.Millisecond, "the holder runs its command, so it holds the only slot")

	waiter := shell(t, host.SlotWrap(slot, folder, model.ShellQuote(marker), "true"))
	require.NoError(t, waiter.Start())
	require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 10*time.Second, 20*time.Millisecond, "the waiting job writes its marker")

	require.NoError(t, waiter.Process.Signal(syscall.SIGTERM))

	var exit *exec.ExitError

	require.ErrorAs(t, waiter.Wait(), &exit)
	assert.Equal(t, 143, exit.ExitCode())
	assert.NoFileExists(t, marker, "a job that is stopped while it waits leaves no marker")
}

// The stop trap belongs to the wait only. Once the job holds a slot, the signal takes its default action and ends the job at once, as it does for a job with no slot.
// A trap that stayed would hold the signal until the command ends, so this command sleeps far longer than the bound of the test.
func TestSlotWrap_AStopSignalWhileTheCommandRunsEndsTheJobAtOnce(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock is not on PATH")
	}

	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	job := shell(t, host.SlotWrap(model.Slot{Class: "bench", Count: 1}, model.ShellQuote(filepath.Join(dir, "slots")), model.ShellQuote(filepath.Join(dir, "m")), "touch "+model.ShellQuote(started)+"; sleep 60"))
	job.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, job.Start())
	t.Cleanup(func() {
		// The sleep outlives the script that the signal ended, so the test stops the whole group. A group with nothing left gives ESRCH, or EPERM on macOS.
		if err := syscall.Kill(-job.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
			t.Errorf("stop the job group: %v", err)
		}
	})

	require.Eventually(t, func() bool { _, err := os.Stat(started); return err == nil }, 10*time.Second, 20*time.Millisecond, "the command starts")
	require.NoError(t, job.Process.Signal(syscall.SIGTERM))

	ended := make(chan error, 1)
	go func() { ended <- job.Wait() }()

	select {
	case err := <-ended:
		var exit *exec.ExitError

		require.ErrorAs(t, err, &exit)

		status, isUnix := exit.Sys().(syscall.WaitStatus)
		require.True(t, isUnix)
		assert.True(t, status.Signaled() && status.Signal() == syscall.SIGTERM, "the job ends by the signal, not by a trap that waits for the command")
	case <-time.After(10 * time.Second):
		t.Fatal("the job ignored the stop signal while its command ran, and would end only when the command ends")
	}
}

func TestSlotWrap_TheScriptRunsTheCommandAndKeepsItsExitStatusWhenASlotIsFree(t *testing.T) {
	t.Parallel()

	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock is not on PATH")
	}

	dir := t.TempDir()
	script := host.SlotWrap(model.Slot{Class: "bench", Count: 1}, model.ShellQuote(filepath.Join(dir, "slots", "bench")), model.ShellQuote(filepath.Join(dir, "m")), "echo ran > "+model.ShellQuote(filepath.Join(dir, "out"))+"; exit 7")

	err := shell(t, script).Run()

	var exit *exec.ExitError

	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 7, exit.ExitCode())

	out, readErr := os.ReadFile(filepath.Join(dir, "out")) //nolint:gosec // the path is inside the temporary folder of the test
	require.NoError(t, readErr)
	assert.Equal(t, "ran\n", string(out))
	assert.NoFileExists(t, filepath.Join(dir, "m"), "a job that took a slot at once never shows the marker")
}
