package slurm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/slurmtest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func TestReadings_ReportsUpOrDrainingWithTheFreeDisk(t *testing.T) {
	t.Parallel()

	tests := map[string]model.HostState{"idle\nmixed\n": model.HostUp, "drained\ndraining*\n": model.HostDraining, "allocated\nidle~\n": model.HostUp}

	for sinfo, want := range tests {
		h, ssh := newScripted(t)
		ssh.Plan(sinfo, "sinfo")
		ssh.Plan(slurmtest.DF, "df")

		got, err := h.Readings(t.Context())

		require.NoError(t, err, sinfo)
		assert.Equal(t, want, got.State, sinfo)
		assert.True(t, got.Disk.Readable)
		assert.Equal(t, uint64(94371840)*1024, got.Disk.FreeBytes)
	}
}

func TestReadings_RefusesWhatTheHostPrintsThatItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ sinfo, df string }{
		"node state unknown": {"sleepy\n", slurmtest.DF},
		"node down":          {"down*\n", slurmtest.DF},
		"no node state":      {"\n", slurmtest.DF},
		"df no data":         {"idle\n", "Filesystem 1024-blocks Used Available Capacity Mounted on\n"},
		"df other header":    {"idle\n", "Filesystem 512-blocks Used Available Capacity Mounted on\n/dev/a 1 1 1 1% /\n"},
		"df seven words":     {"idle\n", "Filesystem 1024-blocks Used Available Capacity Mounted on\nmy disk 1 1 1 1% /\n"},
		"df negative":        {"idle\n", "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/a 1 1 -5 1% /\n"},
		"df overflow":        {"idle\n", "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/a 1 1 18446744073709551615 1% /\n"},
		"df text":            {"idle\n", "df: command not found"},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, ssh := newScripted(t)
			ssh.Plan(tt.sinfo, "sinfo")
			ssh.Plan(tt.df, "df")

			_, err := h.Readings(t.Context())

			require.Error(t, err)
		})
	}
}

func TestReadings_ReportsEachCommandThatFails(t *testing.T) {
	t.Parallel()

	for _, failing := range []string{"sinfo", "df"} {
		h, ssh := newScripted(t)
		ssh.Plan("idle\n", "sinfo")
		ssh.Plan(slurmtest.DF, "df")
		ssh.PlanFail("boom", failing)

		_, err := h.Readings(t.Context())

		require.ErrorIs(t, err, link.ErrCommand, failing)
	}
}

// stubSlot is a slot whose Hold or Release returns the error it is given. The zero value holds and releases nothing.
type stubSlot struct{ hold, release error }

func (s stubSlot) Hold(context.Context, jobwait.Job) error    { return s.hold }
func (s stubSlot) Release(context.Context, jobwait.Job) error { return s.release }

var (
	errHold    = errors.New("hold failed")
	errRelease = errors.New("release failed")
)

func newSlotted(t *testing.T, slot Slot, opts ...Option) (*Queue, *proctest.Scripted) {
	t.Helper()

	ssh := &proctest.Scripted{}
	opts = append([]Option{WithClock(jobwait.Clock{Poll: 1, Now: time.Now, Sleep: proc.Sleep}), WithNonce(fixedNonce)}, opts...)

	return New(link.New("lane-host", ssh), queueConfig(), slot, opts...), ssh
}

func TestSubmit_CancelsTheSlurmJobWhenTheSlotCannotBeHeld(t *testing.T) {
	t.Parallel()

	h, ssh := newSlotted(t, stubSlot{hold: errHold})
	ssh.Plan("77\n", "sbatch")
	ssh.Plan("", "scancel")

	_, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "x"})

	require.ErrorIs(t, err, errHold)
	assert.True(t, ssh.Called("lane-host", "scancel", "77"))
}

func TestSubmit_ReportsBothFailuresWhenTheSlotAndTheCancelFail(t *testing.T) {
	t.Parallel()

	h, ssh := newSlotted(t, stubSlot{hold: errHold})
	ssh.Plan("77\n", "sbatch")
	ssh.PlanFail("no such job", "scancel")

	_, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "x"})

	require.ErrorIs(t, err, errHold)
	require.ErrorIs(t, err, link.ErrCommand)
}

func TestWait_ReturnsTheStateWhenTheSlotCannotBeReleased(t *testing.T) {
	t.Parallel()

	h, ssh := newSlotted(t, stubSlot{release: errRelease}, WithBank(noBank))
	ssh.Plan("COMPLETED\n", "squeue")

	state, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, errRelease)
	assert.Equal(t, model.JobSucceeded, state)
}

func TestWait_ReturnsTheStateTogetherWithTheBankError(t *testing.T) {
	t.Parallel()

	h, ssh := newSlotted(t, stubSlot{release: errRelease}, WithBank(func(context.Context) error { return host.ErrUnbanked }))
	ssh.Plan("COMPLETED\n", "squeue")

	state, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, host.ErrUnbanked)
	require.NotErrorIs(t, err, errRelease, "the slot stays held while the work is not banked")
	assert.Equal(t, model.JobSucceeded, state)
}

func TestWait_ABankedHostWithNoRepositoryHasNothingToBank(t *testing.T) {
	t.Parallel()

	h, ssh := newSlotted(t, stubSlot{}, WithBank(func(context.Context) error { return host.ErrNoRepos }))
	ssh.Plan("COMPLETED\n", "squeue")

	state, err := h.Wait(deadline(t), "5-n")

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
}

func TestWait_ReturnsTheStateWhenTheLoginFolderIsUnknownToTheDefaultBank(t *testing.T) {
	t.Parallel()

	h, ssh := newSlotted(t, stubSlot{})
	ssh.Plan("COMPLETED\n", "squeue")
	ssh.PlanFail("boom", "pwd")

	state, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, link.ErrCommand)
	assert.Equal(t, model.JobSucceeded, state)
}

func TestWait_BanksTheWorkOfTheHeadNodeThroughSSHFromTheLoginFolder(t *testing.T) {
	t.Parallel()

	ssh := &proctest.Scripted{}
	cfg := queueConfig()
	cfg.PrivateRemote = "git@private.test:org/bank.git"
	h := New(link.New("lane-host", ssh), cfg, stubSlot{}, WithClock(jobwait.Clock{Poll: 1, Now: time.Now, Sleep: proc.Sleep}))

	ssh.Plan("/home/lane\n", "pwd")
	ssh.Plan("COMPLETED\n", "squeue")
	ssh.Plan("/home/lane/repo/.git\x00", "find")
	ssh.Plan("", "for-each-ref")
	ssh.Plan("", "worktree", "list")
	ssh.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")

	state, err := h.Wait(deadline(t), "5-n")

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, state)
	assert.True(t, ssh.Called("-o", "BatchMode=yes", "lane-host", "find", "-H", "/home/lane"), "find runs on the head node, from the login folder")
	assert.True(t, ssh.Called("lane-host", "git", "-C", "/home/lane/repo", "worktree", "list", "--porcelain"), "git runs on the head node")
}
