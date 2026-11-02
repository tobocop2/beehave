package skypilot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/host/hosttest"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

func TestHost_ConformsToHost(t *testing.T) {
	t.Parallel()

	hosttest.TestHost(t, newTestHost)
}

func TestHost_IsAHost(t *testing.T) {
	t.Parallel()

	var _ host.Host = (*Host)(nil)
}

func config() model.HostConfig {
	return model.HostConfig{
		Provider: model.HostSkyPilot, IdleMinutes: 45, MaxRun: 12 * time.Hour,
		SkyPilot: model.SkyPilotConfig{Cloud: "gcp", Region: "us-central1", InstanceType: "n2-standard-8"},
	}
}

func newScripted(t *testing.T) (*Host, *proctest.Scripted, *proctest.Scripted) {
	t.Helper()

	sky, ssh := &proctest.Scripted{}, &proctest.Scripted{}

	h, err := New("lane-host", config(), sky, ssh)
	require.NoError(t, err)

	h.poll = time.Millisecond
	h.bank = func(context.Context) error { return nil }
	h.nonce = func() string { return "n" }

	ssh.Plan("", "touch")

	return h, sky, ssh
}

// newBanking returns a host whose Wait banks through the scripted ssh, as in production.
func newBanking(t *testing.T, private string) (*Host, *proctest.Scripted) {
	t.Helper()

	ssh := &proctest.Scripted{}
	cfg := config()
	cfg.PrivateRemote = private

	h, err := New("lane-host", cfg, &proctest.Scripted{}, ssh)
	require.NoError(t, err)

	h.poll = time.Millisecond
	h.nonce = func() string { return "n" }

	planHome(ssh)

	return h, ssh
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
	assert.True(t, ssh.Called("touch", ".obee-banked-lane-host-5-n"))
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

func TestUp_PassesTheIdleMinutesTheRunLimitAndThePlacementToSkyLaunch(t *testing.T) {
	t.Parallel()

	h, sky, _ := newScripted(t)
	sky.Plan("", "launch")

	require.NoError(t, h.Up(context.Background()))
	require.Equal(t, [][]string{{
		"launch", "-y", "-c", "lane-host", "--down", "-i", "45",
		"--config", "gcp.managed_instance_group.run_duration=43200",
		"--infra", "gcp/us-central1", "-t", "n2-standard-8",
	}}, sky.Calls)
}

func TestUp_LeavesOutThePlacementThatTheProjectDoesNotSet(t *testing.T) {
	t.Parallel()

	sky := &proctest.Scripted{}
	sky.Plan("", "launch")

	cfg := config()
	cfg.SkyPilot = model.SkyPilotConfig{Cloud: "gcp"}

	h, err := New("lane-host", cfg, sky, &proctest.Scripted{})
	require.NoError(t, err)
	require.NoError(t, h.Up(context.Background()))
	assert.Equal(t, []string{
		"launch", "-y", "-c", "lane-host", "--down", "-i", "45",
		"--config", "gcp.managed_instance_group.run_duration=43200", "--infra", "gcp",
	}, sky.Calls[0])

	cfg.SkyPilot = model.SkyPilotConfig{}
	h, err = New("lane-host", cfg, sky, &proctest.Scripted{})
	require.NoError(t, err)
	require.NoError(t, h.Up(context.Background()))
	assert.NotContains(t, sky.Calls[1], "--infra")
}

func TestNew_RefusesWhatTheSkyCommandLineCannotTakeSafely(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cluster string
		mutate  func(*model.HostConfig)
	}{
		"no cluster":          {cluster: ""},
		"cluster with a dash": {cluster: "-oProxyCommand=x"},
		"cluster with space":  {cluster: "a b"},
		"cloud with a slash":  {cluster: "c", mutate: func(c *model.HostConfig) { c.SkyPilot.Cloud = "gcp/x" }},
		"instance type flag":  {cluster: "c", mutate: func(c *model.HostConfig) { c.SkyPilot.InstanceType = "--x" }},
		"region without cloud": {cluster: "c", mutate: func(c *model.HostConfig) {
			c.SkyPilot.Cloud = ""
		}},
		"no idle minutes": {cluster: "c", mutate: func(c *model.HostConfig) { c.IdleMinutes = 0 }},
		"no run limit":    {cluster: "c", mutate: func(c *model.HostConfig) { c.MaxRun = time.Millisecond }},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := config()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}

			_, err := New(tt.cluster, cfg, &proctest.Scripted{}, &proctest.Scripted{})
			require.ErrorIs(t, err, ErrConfig)
		})
	}
}

func TestStatus_RefusesAnAnswerItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"not json":                            "Cluster(s) not found",
		"json null":                           "null",
		"json object":                         `{"name": "lane-host", "status": "UP"}`,
		"record without key":                  `[{"name": "lane-host"}]`,
		"record other cluster without status": `[{"name": "other"}]`,
		"cluster is init":                     `[{"name": "lane-host", "status": "INIT"}]`,
		"cluster is stopped":                  `[{"name": "lane-host", "status": "STOPPED"}]`,
		"empty":                               "",
	}

	for name, answer := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, sky, _ := newScripted(t)
			sky.Plan(answer, "status")

			_, err := h.Status(context.Background())
			require.Error(t, err)
		})
	}
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
			require.ErrorIs(t, err, errCommand)
		})
	}
}

func TestDown_RunsSkyDown(t *testing.T) {
	t.Parallel()

	h, sky, _ := newScripted(t)
	sky.Plan("", "down")

	require.NoError(t, h.Down(context.Background()))
	assert.Equal(t, [][]string{{"down", "-y", "lane-host"}}, sky.Calls)
}

func TestSubmit_QuotesTheCommandAndRefusesAnAnswerItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	h, sky, ssh := newScripted(t)
	ssh.Plan("77;cluster1\n", "sbatch")
	sky.Plan("", "exec")

	id, err := h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: "make check; echo 'done'"})
	require.NoError(t, err)
	assert.Equal(t, model.JobID("77-n"), id)
	assert.Equal(t, []string{"-o", "BatchMode=yes", "lane-host", "sbatch", "--parsable", "--job-name=gate", `'--wrap=make check; echo '\''done'\'''`}, ssh.Calls[0])

	for _, answer := range []string{"", "Submitted batch job 77", "77;", "-1", "77\n78"} {
		ssh.Plan(answer, "sbatch")

		_, err = h.Submit(context.Background(), model.JobSpec{Name: "gate", Command: "x"})
		require.ErrorIs(t, err, errJobID, answer)
	}

	_, err = h.Submit(context.Background(), model.JobSpec{Name: "", Command: "x"})
	require.ErrorIs(t, err, errJobFields)

	ssh.PlanFail("boom", "sbatch")

	_, err = h.Submit(context.Background(), model.JobSpec{Name: "g", Command: "x"})
	require.ErrorIs(t, err, errCommand)
}

func TestWait_ReadsTheStateFromSlurmAndMapsEachName(t *testing.T) {
	t.Parallel()

	tests := map[string]model.JobState{
		"COMPLETED": model.JobSucceeded, "FAILED": model.JobFailed, "TIMEOUT": model.JobTimedOut, "OUT_OF_MEMORY": model.JobFailed,
		"NODE_FAIL": model.JobFailed, "CANCELLED": model.JobCanceled, "CANCELLED by 0": model.JobCanceled, "CANCELLED+": model.JobCanceled, //nolint:misspell // Slurm's spelling
	}

	for text, want := range tests {
		t.Run(text, func(t *testing.T) {
			t.Parallel()

			h, _, ssh := newScripted(t)
			ssh.Plan(text+"\n", "squeue")

			got, err := h.Wait(deadline(t), "5-n")
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestWait_AJobThatEndedWithExit124IsTimedOutNotFailed(t *testing.T) {
	t.Parallel()

	tests := map[string]model.JobState{
		"FAILED|124:0\n": model.JobTimedOut, "FAILED|1:0\n": model.JobFailed, "FAILED|0:124\n": model.JobFailed,
	}

	for text, want := range tests {
		h, _, ssh := newScripted(t)
		ssh.PlanFail("slurm_load_jobs error: Invalid job id specified", "squeue")
		ssh.Plan(text, "sacct")

		got, err := h.Wait(deadline(t), "5-n")
		require.NoError(t, err, text)
		assert.Equal(t, want, got, text)
	}
}

func TestWait_KeepsPollingWhileTheJobIsPendingOrRunning(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("PENDING\n", "squeue")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := h.Wait(ctx, "5-n")
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWait_ReadsSacctWhenSqueueForgotTheJob(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.PlanFail("slurm_load_jobs error: Invalid job id specified", "squeue")
	ssh.Plan("FAILED\n", "sacct")

	got, err := h.Wait(deadline(t), "5-n")
	require.NoError(t, err)
	assert.Equal(t, model.JobFailed, got)

	ssh.Plan("", "squeue")

	got, err = h.Wait(deadline(t), "5-n")
	require.NoError(t, err)
	assert.Equal(t, model.JobFailed, got)
}

func TestWait_RefusesWhatItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*proctest.Scripted){
		"squeue state unknown": func(ssh *proctest.Scripted) { ssh.Plan("MYSTERY\n", "squeue") },
		"squeue two lines":     func(ssh *proctest.Scripted) { ssh.Plan("RUNNING\nRUNNING\n", "squeue") },
		"squeue lower case":    func(ssh *proctest.Scripted) { ssh.Plan("completed\n", "squeue") },
		"sacct fails": func(ssh *proctest.Scripted) {
			ssh.PlanFail("x", "squeue")
			ssh.PlanFail("x", "sacct")
		},
		"sacct unknown state": func(ssh *proctest.Scripted) {
			ssh.PlanFail("x", "squeue")
			ssh.Plan("MYSTERY\n", "sacct")
		},
	}

	for name, plan := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, _, ssh := newScripted(t)
			plan(ssh)

			state, err := h.Wait(deadline(t), "5-n")
			require.Error(t, err)
			assert.Equal(t, model.JobUnknown, state)
		})
	}
}

func TestCancel_WritesTheMarkerOfTheSlotJobAfterScancel(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("", "scancel")
	ssh.Plan("CANCELLED\n", "squeue") //nolint:misspell // Slurm's spelling

	require.NoError(t, h.Cancel(deadline(t), "5-n"))
	require.Len(t, ssh.Calls, 3)
	assert.Equal(t, []string{"-o", "BatchMode=yes", "lane-host", "scancel", "5"}, ssh.Calls[0])
	assert.Equal(t, []string{"-o", "BatchMode=yes", "lane-host", "touch", ".obee-banked-lane-host-5-n"}, ssh.Calls[2])
}

func TestLog_ReadsTheTailOfTheSlurmLogOfTheJob(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("line\nobee-gate-done\n", "tail")

	log, err := h.Log(deadline(t), "5-n")

	require.NoError(t, err)
	assert.Equal(t, "line\nobee-gate-done\n", log)
	assert.Equal(t, []string{"-o", "BatchMode=yes", "lane-host", "tail", "-c", "16384", "slurm-5.out"}, ssh.Calls[0])

	_, err = h.Log(deadline(t), "5; rm -rf x")
	require.Error(t, err)
	assert.Len(t, ssh.Calls, 1)
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

	require.ErrorIs(t, h.Cancel(context.Background(), "5-n"), errCommand)
	assert.False(t, ssh.Called("touch"))
}

func TestCancel_ReturnsTheErrorOfTheMarkerWrite(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("", "scancel")
	ssh.PlanFail("read-only", "touch")

	require.ErrorIs(t, h.Cancel(context.Background(), "5-n"), errCommand)
}

func TestDrainAndResume_UpdateEveryNode(t *testing.T) {
	t.Parallel()

	h, _, ssh := newScripted(t)
	ssh.Plan("n1\nn2\nn1\n", "sinfo")
	ssh.Plan("", "scontrol")

	require.NoError(t, h.Drain(context.Background()))
	require.NoError(t, h.Resume(context.Background()))

	var updates []string

	for _, call := range ssh.Calls {
		if call[3] != "sinfo" {
			updates = append(updates, strings.Join(call[3:], " "))
		}
	}

	assert.Equal(t, []string{
		"scontrol update NodeName=n1 State=DRAIN Reason=obee", "scontrol update NodeName=n2 State=DRAIN Reason=obee",
		"scontrol update NodeName=n1 State=RESUME", "scontrol update NodeName=n2 State=RESUME",
	}, updates)
}

func TestDrain_RefusesWhatItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*proctest.Scripted){
		"no node":          func(ssh *proctest.Scripted) { ssh.Plan("\n", "sinfo") },
		"node with a flag": func(ssh *proctest.Scripted) { ssh.Plan("-x\n", "sinfo") },
		"node with space":  func(ssh *proctest.Scripted) { ssh.Plan("a b\n", "sinfo") },
		"sinfo fails":      func(ssh *proctest.Scripted) { ssh.PlanFail("x", "sinfo") },
		"scontrol fails": func(ssh *proctest.Scripted) {
			ssh.Plan("n1\n", "sinfo")
			ssh.PlanFail("x", "scontrol")
		},
	}

	for name, plan := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, _, ssh := newScripted(t)
			plan(ssh)

			require.Error(t, h.Drain(context.Background()))
			require.Error(t, h.Resume(context.Background()))
		})
	}
}

func TestQuote_KeepsASafeWordAndQuotesTheRest(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "abc-1.2/x=y", quote("abc-1.2/x=y"))
	assert.Equal(t, "''", quote(""))
	assert.Equal(t, "'a b'", quote("a b"))
	assert.Equal(t, `'it'\''s'`, quote("it's"))
	assert.Equal(t, `'$(x)'`, quote("$(x)"))
}
