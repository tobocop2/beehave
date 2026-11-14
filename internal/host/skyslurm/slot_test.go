package skyslurm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/slurm"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

var errBank = errors.New("bank failed")

// testDeadline is the longest a test that can block may run. A parser that is wrong makes Wait poll for ever, so the test fails here instead.
const testDeadline = 5 * time.Second

// deadline returns a context that ends after testDeadline or when the test ends.
func deadline(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), testDeadline)
	t.Cleanup(cancel)

	return ctx
}

// planHome answers the login folder and the marker write that a banking Wait makes.
func planHome(ssh *proctest.Scripted) {
	ssh.Plan("/home/lane\n", "pwd")
	ssh.Plan("", "touch")
}

func TestSubmit_HoldsTheSkyPilotSlotUntilTheWorkIsBanked(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newScripted(t)
	ssh.Plan("77\n", "sbatch")
	sky.Plan("", "exec")

	id, err := h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: "make check"})

	require.NoError(t, err)
	assert.Equal(t, model.JobID("77-n"), id, "the id holds the Slurm job id and the nonce")
	assert.Equal(t, [][]string{{
		"exec", "lane-host", "-d",
		"cd ~ && timeout 43200 sh -c 'until test -e .beehave-banked-lane-host-77-n; do sleep 5; done'",
	}}, sky.Calls, "the slot lasts for at most max_run")
}

// newFakeSlots returns a host that is up, over a fake that keeps the files of the login folder and the slot jobs.
func newFakeSlots(t *testing.T) (*skypilot.Host, *fakeHost) {
	t.Helper()

	f := newFakeHost(hosttest.Working)

	h, err := newHost(fakeCluster, model.HostConfig{Provider: model.HostSkyPilot, IdleMinutes: 60, MaxRun: 12 * time.Hour}, f.sky(), f.SSH(), fastWaits()...)
	require.NoError(t, err)

	require.NoError(t, h.Up(t.Context()))

	return h, f
}

func TestCancel_EndsTheSlotJob(t *testing.T) {
	t.Parallel()

	h, f := newFakeSlots(t)
	ctx := deadline(t)

	id, err := h.Submit(ctx, model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})
	require.NoError(t, err)
	require.Equal(t, 1, f.WaitingSlots(), "Submit starts one slot job")

	require.NoError(t, h.Cancel(ctx, id))

	assert.Zero(t, f.WaitingSlots(), "a canceled job leaves no slot job that holds the cluster")
}

// newSecondHandle returns a host over the same fake as the first, as after a restart: it never submitted anything.
func newSecondHandle(t *testing.T, f *fakeHost) *skypilot.Host {
	t.Helper()

	h, err := newHost(fakeCluster, model.HostConfig{Provider: model.HostSkyPilot, IdleMinutes: 60, MaxRun: 12 * time.Hour}, f.sky(), f.SSH(), fastWaits()...)
	require.NoError(t, err)

	return h
}

func TestWait_AHandleThatNeverSubmittedEndsTheSlotJobOfTheIdItIsGiven(t *testing.T) {
	t.Parallel()

	first, f := newFakeSlots(t)
	ctx := deadline(t)

	id, err := first.Submit(ctx, model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})
	require.NoError(t, err)
	require.Equal(t, 1, f.WaitingSlots())

	_, err = newSecondHandle(t, f).Wait(ctx, id)

	require.NoError(t, err)
	assert.Zero(t, f.WaitingSlots(), "the marker carries the nonce of the Submit")
}

func TestCancel_AHandleThatNeverSubmittedEndsTheSlotJobOfTheIdItIsGiven(t *testing.T) {
	t.Parallel()

	first, f := newFakeSlots(t)
	ctx := deadline(t)

	id, err := first.Submit(ctx, model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})
	require.NoError(t, err)

	require.NoError(t, newSecondHandle(t, f).Cancel(ctx, id))

	assert.Zero(t, f.WaitingSlots())
}

func TestSubmit_AReusedJobIdNeverMatchesAnOldMarker(t *testing.T) {
	t.Parallel()

	h, f := newFakeSlots(t)
	ctx := deadline(t)

	first, err := h.Submit(ctx, model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})
	require.NoError(t, err)

	_, err = h.Wait(ctx, first)
	require.NoError(t, err)
	require.Zero(t, f.WaitingSlots(), "Wait ends the slot job")

	f.Mu.Lock()
	f.ReuseID = true
	f.Mu.Unlock()

	second, err := h.Submit(ctx, model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})
	require.NoError(t, err)
	require.NotEqual(t, first, second, "Slurm gave the job number again, but the nonce makes a new id")

	assert.Equal(t, 1, f.WaitingSlots(), "the old marker does not end the slot job of the new job")
}

func TestSubmit_CancelsTheSlurmJobWhenTheSlotCannotBeHeld(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newScripted(t)
	ssh.Plan("77\n", "sbatch")
	ssh.Plan("", "scancel")
	sky.PlanFail("boom", "exec")

	_, err := h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: "x"})

	require.ErrorIs(t, err, link.ErrCommand)
	assert.True(t, ssh.Called("scancel", "77"), "a job with no slot is not left to run")
}

func TestSubmit_ReportsBothFailuresWhenTheSlotAndTheCancelFail(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newScripted(t)
	ssh.Plan("77\n", "sbatch")
	ssh.PlanFail("no such job", "scancel")
	sky.PlanFail("boom", "exec")

	_, err := h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: "x"})

	require.ErrorContains(t, err, "sky exec")
	require.ErrorContains(t, err, "scancel")
}

func TestWait_MarksTheJobBankedAfterTheBank(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("COMPLETED\n", "squeue")
	ssh.Plan("", "touch")

	got, err := h.Wait(deadline(t), "5-n")

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, got)
	assert.True(t, ssh.Called("lane-host", "touch", ".beehave-banked-lane-host-5-n"))
}

func TestWait_WritesNoMarkerWhenTheBankFails(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t, slurm.WithBank(func(context.Context) error { return errBank }))
	ssh.Plan("COMPLETED\n", "squeue")
	ssh.Plan("", "touch")

	_, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, errBank)
	assert.False(t, ssh.Called("touch"), "the slot stays held while work is unbanked")
}

func TestWait_ReturnsTheStateWhenTheMarkerCannotBeWritten(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("FAILED\n", "squeue")
	ssh.PlanFail("read-only", "touch")

	got, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, link.ErrCommand)
	assert.Equal(t, model.JobFailed, got)
}

func TestWait_ABankedHostWithNoRepositoryHasNothingToBank(t *testing.T) {
	t.Parallel()

	h, ssh := newBanking(t, "")
	planHome(ssh)
	ssh.Plan("COMPLETED\n", "squeue")
	ssh.Plan("", "find")

	got, err := h.Wait(deadline(t), "5-n")

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, got)
}

func TestWait_ReturnsTheStateTogetherWithTheBankError(t *testing.T) {
	t.Parallel()

	h, ssh := newBanking(t, "")
	planHome(ssh)
	ssh.Plan("FAILED\n", "squeue")
	ssh.Plan("/home/lane/repo/.git\x00", "find")
	ssh.Plan("", "for-each-ref")
	ssh.Plan("", "worktree", "list")
	ssh.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")
	ssh.PlanContaining("commit\t1111111111111111111111111111111111111111\t\t\trefs/heads/main\n", "%(objecttype)")

	got, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, host.ErrUnbanked)
	assert.Equal(t, model.JobFailed, got, "the caller can tell a failed gate from a passed one")
}

func TestWait_ReturnsTheStateWhenTheLoginFolderIsUnknown(t *testing.T) {
	t.Parallel()

	h, ssh := newBanking(t, "")
	ssh.PlanFail("boom", "pwd")
	ssh.Plan("COMPLETED\n", "squeue")

	got, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, link.ErrCommand)
	assert.Equal(t, model.JobSucceeded, got)
	assert.False(t, ssh.Called("find"), "nothing is scanned from a root that is unknown")
	assert.False(t, ssh.Called("touch"))
}

func TestWait_ScansTheLoginFolderByItsAbsolutePath(t *testing.T) {
	t.Parallel()

	h, ssh := newBanking(t, "")
	planHome(ssh)
	ssh.Plan("COMPLETED\n", "squeue")
	ssh.Plan("", "find")

	_, err := h.Wait(deadline(t), "5-n")

	require.NoError(t, err)
	assert.True(t, ssh.Called("lane-host", "find", "-H", "/home/lane"))
	assert.False(t, ssh.Called("find", "-H", "."))
}
