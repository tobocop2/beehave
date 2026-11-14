package skyslurm

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/slurm"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

func TestHost_ConformsToHost(t *testing.T) {
	t.Parallel()

	hosttest.TestHost(t, newTestHost)
}

// hostConfig is the host of a project that asks SkyPilot for a placement.
func hostConfig() model.HostConfig {
	return model.HostConfig{
		Provider: skypilot.Name, IdleMinutes: 45, MaxRun: 12 * time.Hour,
		SkyPilot: model.SkyPilotConfig{Cloud: "gcp", Region: "us-central1", InstanceType: "n2-standard-8"},
	}
}

// newHost builds the SkyPilot host over the real Slurm queue, as cmd/beehave does.
func newHost(cluster string, cfg model.HostConfig, sky, ssh proc.Runner, opts ...slurm.Option) (*skypilot.Host, error) {
	queue := func(l *link.Link, config model.HostConfig, slot skypilot.Slot) skypilot.Queue {
		return slurm.New(l, config, slot, opts...)
	}

	return skypilot.New(cluster, cfg, sky, ssh, queue)
}

// newScripted returns a host over scripted sky and ssh runners. A later option replaces an earlier one.
func newScripted(t *testing.T, opts ...slurm.Option) (*skypilot.Host, *proctest.Scripted, *proctest.Scripted) {
	t.Helper()

	sky, ssh := &proctest.Scripted{}, &proctest.Scripted{}

	opts = append(append(fastWaits(), slurm.WithNonce(func() string { return "n" })), opts...)

	h, err := newHost("lane-host", hostConfig(), sky, ssh, opts...)
	require.NoError(t, err)

	ssh.Plan("", "touch")

	return h, sky, ssh
}

// newBanking returns a host whose Wait banks through the scripted ssh, as in production.
func newBanking(t *testing.T, private string) (*skypilot.Host, *proctest.Scripted) {
	t.Helper()

	h, _, ssh := newBankingWithSky(t, private)

	return h, ssh
}

// newBankingWithSky is newBanking with the scripted sky runner that the host runs.
func newBankingWithSky(t *testing.T, private string) (*skypilot.Host, *proctest.Scripted, *proctest.Scripted) {
	t.Helper()

	sky, ssh := &proctest.Scripted{}, &proctest.Scripted{}
	cfg := hostConfig()
	cfg.PrivateRemote = private

	h, err := newHost("lane-host", cfg, sky, ssh, slurm.WithClock(jobwait.Clock{Poll: time.Millisecond, Now: time.Now, Sleep: proc.Sleep}), slurm.WithNonce(func() string { return "n" }))
	require.NoError(t, err)

	planHome(ssh)

	return h, sky, ssh
}

func TestWait_BanksTheWorkOfTheHeadNodeBeforeItReturnsAFinalState(t *testing.T) {
	t.Parallel()

	h, ssh := newBanking(t, "git@private.test:org/bank.git")
	ssh.Plan("COMPLETED\n", "squeue")
	ssh.Plan("/home/lane/repo/.git\x00", "find")
	ssh.Plan("", "for-each-ref")
	ssh.Plan("", "worktree", "list")
	ssh.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")

	got, err := h.Wait(deadline(t), "5-n")

	require.NoError(t, err)
	assert.Equal(t, model.JobSucceeded, got)
	assert.True(t, ssh.Called("-o", "BatchMode=yes", "lane-host", "find", "-H", "/home/lane"), "find runs on the head node, from the login folder")
	assert.True(t, ssh.Called("lane-host", "git", "-C", "/home/lane/repo", "worktree", "list", "--porcelain"), "git runs on the head node")
	assert.True(t, ssh.Called("touch", ".beehave-banked-lane-host-5-n"))
}

func TestWait_RefusesAFinalStateWhileTheHostHoldsUnbankedWork(t *testing.T) {
	t.Parallel()

	h, ssh := newBanking(t, "")
	ssh.Plan("COMPLETED\n", "squeue")
	ssh.Plan("./repo/.git\x00", "find")
	ssh.Plan("", "for-each-ref")
	ssh.PlanContaining("commit\t1111111111111111111111111111111111111111\t\t\trefs/heads/main\n", "%(objecttype)")
	ssh.Plan("", "worktree", "list")
	ssh.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")

	_, err := h.Wait(deadline(t), "5-n")

	require.ErrorIs(t, err, host.ErrUnbanked)
	require.ErrorContains(t, err, "job 5 is succeeded")
}

func TestWait_DoesNotBankWhileTheJobRuns(t *testing.T) {
	t.Parallel()

	h, ssh := newBanking(t, "")
	ssh.Plan("RUNNING\n", "squeue")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := h.Wait(ctx, "5-n")

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.False(t, ssh.Called("find"))
}

func TestStatus_AcceptsThePrefixedStatusAndAnEmptyList(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newScripted(t)
	sky.Plan("[]", "status")

	got, err := h.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, model.HostAbsent, got.State)
	assert.Empty(t, ssh.Calls)

	sky.Plan(`[{"name": "lane-host", "status": "ClusterStatus.UP"}]`, "status")
	ssh.Plan("idle\n", "sinfo")
	ssh.Plan(fakeDF, "df")

	got, err = h.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, model.HostStatus{State: model.HostUp, Disk: model.DiskReading{FreeBytes: 94371840 * 1024, Readable: true}}, got)
}

func TestStatus_RefusesWhatTheHostPrintsThatItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ sinfo, df string }{
		"node state unknown": {"sleepy\n", fakeDF},
		"no node state":      {"\n", fakeDF},
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

			h, sky, ssh := newScripted(t)
			sky.Plan(`[{"name": "lane-host", "status": "UP"}]`, "status")
			ssh.Plan(tt.sinfo, "sinfo")
			ssh.Plan(tt.df, "df")

			_, err := h.Status(context.Background())
			require.Error(t, err)
		})
	}
}

func TestStatus_ReportsEachCommandThatFails(t *testing.T) {
	t.Parallel()

	for _, failing := range []string{"status", "sinfo", "df"} {
		t.Run(failing, func(t *testing.T) {
			t.Parallel()

			h, sky, ssh := newScripted(t)
			sky.Plan(`[{"name": "lane-host", "status": "UP"}]`, "status")
			ssh.Plan("idle\n", "sinfo")
			ssh.Plan(fakeDF, "df")

			if failing == "status" {
				sky.PlanFail("boom", "status")
			} else {
				ssh.PlanFail("boom", failing)
			}

			_, err := h.Status(context.Background())
			require.ErrorIs(t, err, link.ErrCommand)
		})
	}
}

func TestWorkRootAndLog_AreReadFromTheHeadNodeOfTheCluster(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("/home/lane\n", "pwd")
	ssh.Plan("line\n", "tail")

	root, err := h.WorkRoot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/home/lane", root)

	log, err := h.Log(context.Background(), "5-n")
	require.NoError(t, err)
	assert.Equal(t, "line\n", log)
	assert.True(t, ssh.Called("-o", "BatchMode=yes", "lane-host", "tail", "-c", "16384", "slurm-5.out"))
}

func TestCancel_WritesTheMarkerOfTheSlotJobAfterScancel(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("", "scancel")
	ssh.Plan("CANCELLED\n", "squeue") //nolint:misspell // Slurm's spelling

	require.NoError(t, h.Cancel(deadline(t), "5-n"))
	require.Len(t, ssh.Calls, 3)
	assert.Equal(t, []string{"-o", "BatchMode=yes", "lane-host", "scancel", "5"}, ssh.Calls[0])
	assert.Equal(t, []string{"-o", "BatchMode=yes", "lane-host", "touch", ".beehave-banked-lane-host-5-n"}, ssh.Calls[2])
}

func TestCancel_BanksTheWorkOfTheCanceledJobBeforeTheSlotEnds(t *testing.T) {
	t.Parallel()

	h, ssh := newBanking(t, "")
	ssh.Plan("", "scancel")
	ssh.Plan("CANCELLED\n", "squeue") //nolint:misspell // Slurm's spelling
	ssh.Plan("./repo/.git\x00", "find")
	ssh.Plan("", "for-each-ref")
	ssh.PlanContaining("commit\t1111111111111111111111111111111111111111\t\t\trefs/heads/main\n", "%(objecttype)")
	ssh.Plan("", "worktree", "list")
	ssh.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")

	err := h.Cancel(deadline(t), "5-n")

	require.ErrorIs(t, err, host.ErrUnbanked)
	assert.False(t, ssh.Called("touch"), "the slot holds while the canceled job's work is not banked")
}

func TestCancel_WaitsUntilTheJobIsFinalBeforeItBanks(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("", "scancel")
	ssh.Plan("RUNNING\n", "squeue")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	require.ErrorIs(t, h.Cancel(ctx, "5-n"), context.DeadlineExceeded)
	assert.False(t, ssh.Called("touch"), "a job that still runs after scancel keeps its slot")
}

func TestCancel_KeepsTheSlotJobWhenScancelFails(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.PlanFail("no such job", "scancel")

	require.ErrorIs(t, h.Cancel(context.Background(), "5-n"), link.ErrCommand)
	assert.False(t, ssh.Called("touch"))
}

func TestCancel_ReturnsTheErrorOfTheMarkerWrite(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("", "scancel")
	ssh.PlanFail("read-only", "touch")

	require.ErrorIs(t, h.Cancel(context.Background(), "5-n"), link.ErrCommand)
}
