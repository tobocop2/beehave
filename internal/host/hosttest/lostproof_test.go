package hosttest_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/host"
	"github.com/tobocop2/beehest/internal/host/hosttest"
	"github.com/tobocop2/beehest/internal/host/link"
	"github.com/tobocop2/beehest/internal/host/skypilot"
	"github.com/tobocop2/beehest/internal/host/slurm"
	"github.com/tobocop2/beehest/internal/host/slurmtest"
	"github.com/tobocop2/beehest/internal/host/ssh"
	"github.com/tobocop2/beehest/internal/host/sshtest"
	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/proc/proctest"
)

// lostSSH returns the ssh adapter over a fake machine and a job that it ran.
func lostSSH(t *testing.T) (host.Host, hosttest.LostJob) {
	t.Helper()

	m := sshtest.New(hosttest.Working)
	cfg := model.HostConfig{
		Provider: ssh.Name, MaxRun: 2 * time.Hour, StaleMinutes: 20, PrivateRemote: "private",
		Block: model.NewBlock("test", []model.BlockEntry{model.TextEntry(ssh.BlockUser, "lane"), model.TextEntry(ssh.BlockWorkRoot, sshtest.Root+"/work")}),
	}

	h, err := ssh.New("gates", cfg, m.SSH())
	require.NoError(t, err)

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})
	require.NoError(t, err)

	return h, hosttest.LostJob{
		ID:       id,
		LinkDown: func() { m.Mu.Lock(); defer m.Mu.Unlock(); m.Drops = -1 },
		LinkUp:   func() { m.Mu.Lock(); defer m.Mu.Unlock(); m.Drops = 0 },
		Proof:    model.LostStopped,
	}
}

// lostSlurm returns the Slurm adapter over a fake cluster and a job that it queued.
func lostSlurm(t *testing.T) (host.Host, hosttest.LostJob) {
	t.Helper()

	f := slurmtest.New(hosttest.Working)
	f.Up = true
	cfg := model.HostConfig{Provider: slurm.Name, MaxRun: 2 * time.Hour, StaleMinutes: 20, PrivateRemote: "private"}

	h, err := slurm.NewHost(slurmtest.Target, cfg, f.SSH())
	require.NoError(t, err)

	id, err := h.Submit(t.Context(), model.JobSpec{Name: "gate", Command: hosttest.PassingCommand})
	require.NoError(t, err)

	return h, hosttest.LostJob{
		ID:       id,
		LinkDown: func() { f.Mu.Lock(); defer f.Mu.Unlock(); f.Drops = -1 },
		LinkUp:   func() { f.Mu.Lock(); defer f.Mu.Unlock(); f.Drops = 0 },
		Proof:    model.LostEnded,
	}
}

// lostSkyPilot returns the SkyPilot adapter. It reads no proof yet (issue #401), so its link has no switch that changes the answer.
func lostSkyPilot(t *testing.T) (host.Host, hosttest.LostJob) {
	t.Helper()

	cfg := model.HostConfig{Provider: skypilot.Name, IdleMinutes: 60, MaxRun: 2 * time.Hour}

	h, err := skypilot.New("lane-host", cfg, &proctest.Scripted{}, &proctest.Scripted{}, func(l *link.Link, config model.HostConfig, slot skypilot.Slot) skypilot.Queue {
		return slurm.New(l, config, slot, slurm.WithLimit(config.MaxRun))
	})
	require.NoError(t, err)

	return h, hosttest.LostJob{ID: "1-n", LinkDown: func() {}, LinkUp: func() {}}
}

func TestHostConformance_ALostJobGivesNoProofWhileTheLinkIsDownOnEveryAdapter(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func(*testing.T) (host.Host, hosttest.LostJob){
		string(ssh.Name):      lostSSH,
		string(slurm.Name):    lostSlurm,
		string(skypilot.Name): lostSkyPilot,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, job := build(t)

			hosttest.CheckLostProof(t, h, job)
		})
	}
}
