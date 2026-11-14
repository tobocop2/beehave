package skypilot

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
	"github.com/tobocop2/beehave/internal/model"
)

const upCluster = `[{"name": "lane-host", "status": "UP"}]`

func TestHost_IsAHost(t *testing.T) {
	t.Parallel()

	var _ host.Host = (*Host)(nil)
}

func TestUp_PassesTheIdleMinutesTheRunLimitAndThePlacementToSkyLaunch(t *testing.T) {
	t.Parallel()

	h, _, sky, _ := newHost(t, hostConfig(placement()))
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

	tests := map[string]struct {
		placed *model.SkyPilotConfig
		infra  []string
	}{
		"cloud_without_region": {placed: &model.SkyPilotConfig{Cloud: "gcp"}, infra: []string{"--infra", "gcp"}},
		"no_placement":         {placed: nil},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, _, sky, _ := newHost(t, hostConfig(tt.placed))
			sky.Plan("", "launch")

			require.NoError(t, h.Up(context.Background()))
			assert.Equal(t, append([]string{
				"launch", "-y", "-c", "lane-host", "--down", "-i", "45",
				"--config", "gcp.managed_instance_group.run_duration=43200",
			}, tt.infra...), sky.Calls[0])
		})
	}
}

func TestUp_ReportsTheFailureOfSkyLaunch(t *testing.T) {
	t.Parallel()

	h, _, sky, _ := newHost(t, hostConfig(nil))
	sky.PlanFail("boom", "launch")

	require.ErrorIs(t, h.Up(context.Background()), link.ErrCommand)
}

func TestNew_RefusesWhatTheSkyCommandLineCannotTakeSafely(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cluster string
		mutate  func(*model.HostConfig)
	}{
		"no_cluster":          {cluster: ""},
		"cluster_with_a_dash": {cluster: "-oProxyCommand=x"},
		"cluster_with_space":  {cluster: "a b"},
		"cloud_with_a_slash":  {cluster: "c", mutate: func(c *model.HostConfig) { c.SkyPilot = model.SkyPilotConfig{Cloud: "gcp/x"} }},
		"instance_type_flag":  {cluster: "c", mutate: func(c *model.HostConfig) { c.SkyPilot = model.SkyPilotConfig{InstanceType: "--x"} }},
		"region_without_cloud": {cluster: "c", mutate: func(c *model.HostConfig) {
			c.SkyPilot = model.SkyPilotConfig{Region: "us-central1"}
		}},
		"no_idle_minutes": {cluster: "c", mutate: func(c *model.HostConfig) { c.IdleMinutes = 0 }},
		"no_run_limit":    {cluster: "c", mutate: func(c *model.HostConfig) { c.MaxRun = time.Millisecond }},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := hostConfig(placement())
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}

			_, err := New(tt.cluster, cfg, nil, nil, func(*link.Link, model.HostConfig, Slot) Queue { return &fakeQueue{} })
			require.ErrorIs(t, err, ErrConfig)
		})
	}
}

func TestStatus_RefusesAnAnswerItDoesNotRecognise(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"not_json":                            "Cluster(s) not found",
		"json_null":                           "null",
		"json_object":                         `{"name": "lane-host", "status": "UP"}`,
		"record_without_key":                  `[{"name": "lane-host"}]`,
		"record_other_cluster_without_status": `[{"name": "other"}]`,
		"empty":                               "",
	}

	for name, answer := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h, _, sky, _ := newHost(t, hostConfig(nil))
			sky.Plan(answer, "status")

			_, err := h.Status(context.Background())
			require.ErrorIs(t, err, errClusterList)
		})
	}
}

func TestStatus_NamesTheStateOfAClusterThatIsNotUp(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"INIT", "STOPPED"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			h, _, sky, ssh := newHost(t, hostConfig(nil))
			sky.Plan(`[{"name": "lane-host", "status": "`+state+`"}]`, "status")

			_, err := h.Status(context.Background())

			require.ErrorIs(t, err, errClusterUp)
			require.ErrorContains(t, err, state)
			assert.Empty(t, ssh.Calls, "no command runs on a cluster that is not up")
		})
	}
}

func TestStatus_AnEmptyListOrAnotherClusterMeansAbsent(t *testing.T) {
	t.Parallel()

	for _, answer := range []string{"[]", `[{"name": "other", "status": "UP"}]`} {
		h, queue, sky, _ := newHost(t, hostConfig(nil))
		queue.err = errors.New("the queue must not be read")

		sky.Plan(answer, "status")

		got, err := h.Status(context.Background())
		require.NoError(t, err)
		assert.Equal(t, model.HostAbsent, got.State)
	}
}

func TestStatus_AcceptsThePrefixedStatusAndReportsTheReadingsOfTheQueue(t *testing.T) {
	t.Parallel()

	h, queue, sky, _ := newHost(t, hostConfig(nil))
	queue.status = model.HostStatus{State: model.HostUp, Disk: model.DiskReading{FreeBytes: 7, Readable: true}}
	sky.Plan(`[{"name": "lane-host", "status": "ClusterStatus.UP"}]`, "status")

	got, err := h.Status(context.Background())

	require.NoError(t, err)
	assert.Equal(t, queue.status, got)
}

func TestStatus_ReportsTheFailureOfSkyStatusAndOfTheQueue(t *testing.T) {
	t.Parallel()

	h, queue, sky, _ := newHost(t, hostConfig(nil))
	sky.PlanFail("boom", "status")

	_, err := h.Status(context.Background())
	require.ErrorIs(t, err, link.ErrCommand)

	errRead := errors.New("sinfo failed")
	queue.err = errRead

	sky.Plan(upCluster, "status")

	_, err = h.Status(context.Background())
	require.ErrorIs(t, err, errRead)
}

func TestDown_RunsSkyDown(t *testing.T) {
	t.Parallel()

	h, _, sky, _ := newHost(t, hostConfig(nil))
	sky.Plan("", "down")

	require.NoError(t, h.Down(context.Background()))
	assert.Equal(t, [][]string{{"down", "-y", "lane-host"}}, sky.Calls)
}

// newTeardown returns a host whose head node holds nothing unbanked and whose cluster sky no longer lists after the delete.
func newTeardown(t *testing.T) (*Host, *scripts) {
	t.Helper()

	h, _, sky, ssh := newHost(t, hostConfig(nil))
	ssh.Plan("/home/lane/repo/.git\x00", "find")
	ssh.Plan("", "for-each-ref")
	ssh.Plan("", "worktree", "list")
	ssh.PlanResult(model.CommandResult{ExitCode: 1}, "config", "--get-regexp")
	sky.Plan("", "down")
	sky.Plan(`[{"name": "other", "status": "UP"}]`, "status")

	return h, &scripts{sky: sky, ssh: ssh}
}

func TestTeardown_BanksOnTheHeadNodeThenDeletesTheCluster(t *testing.T) {
	t.Parallel()

	h, run := newTeardown(t)

	require.NoError(t, h.Teardown(t.Context()))
	assert.True(t, run.ssh.Called("lane-host", "git", "-C", "/home/lane/repo", "worktree", "list", "--porcelain"), "git runs on the head node")
	assert.Equal(t, [][]string{{"down", "-y", "lane-host"}, {"status", "-r", "-o", "json"}}, run.sky.Calls, "the delete is followed by a read of the status")
}

func TestTeardown_KeepsTheClusterWhileTheHeadNodeHoldsUnbankedWork(t *testing.T) {
	t.Parallel()

	h, run := newTeardown(t)
	run.ssh.PlanContaining("commit\t1111111111111111111111111111111111111111\t\t\trefs/heads/main\n", "%(objecttype)")

	err := h.Teardown(t.Context())

	require.ErrorIs(t, err, host.ErrUnbanked)
	assert.Empty(t, run.sky.Calls, "sky down does not run")
}

func TestSlot_HoldStartsASkyJobThatWaitsForTheMarkerForAtMostMaxRun(t *testing.T) {
	t.Parallel()

	_, queue, sky, _ := newHost(t, hostConfig(nil))
	sky.Plan("", "exec")

	require.NoError(t, queue.slot.Hold(context.Background(), jobwait.Job{ID: "77", Nonce: "n"}))
	assert.Equal(t, [][]string{{
		"exec", "lane-host", "-d",
		"cd ~ && timeout 43200 sh -c 'until test -e .beehave-banked-lane-host-77-n; do sleep 5; done'",
	}}, sky.Calls)
}

func TestSlot_ReleaseWritesTheMarkerOnTheHeadNode(t *testing.T) {
	t.Parallel()

	_, queue, _, ssh := newHost(t, hostConfig(nil))
	ssh.Plan("", "touch")

	require.NoError(t, queue.slot.Release(context.Background(), jobwait.Job{ID: "77", Nonce: "n"}))
	assert.True(t, ssh.Called("lane-host", "touch", ".beehave-banked-lane-host-77-n"))
}

func TestSlot_ReportsTheFailureOfSkyExec(t *testing.T) {
	t.Parallel()

	_, queue, sky, _ := newHost(t, hostConfig(nil))
	sky.PlanFail("boom", "exec")

	require.ErrorIs(t, queue.slot.Hold(context.Background(), jobwait.Job{ID: "1"}), link.ErrCommand)
}

func TestQueueReads_AFailedReadOfTheQueueReachesTheCallerOfTheHost(t *testing.T) {
	t.Parallel()

	reads := map[string]func(*Host) error{
		"Submit": func(h *Host) error {
			_, err := h.Submit(context.Background(), model.JobSpec{Name: "gate-p"})

			return err
		},
		"Wait": func(h *Host) error {
			_, err := h.Wait(context.Background(), "1")

			return err
		},
		"WorkRoot": func(h *Host) error {
			_, err := h.WorkRoot(context.Background())

			return err
		},
		"Log": func(h *Host) error {
			_, err := h.Log(context.Background(), "1")

			return err
		},
	}

	for name, read := range reads {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			errRead := errors.New("the queue could not be read")
			h, queue, _, _ := newHost(t, hostConfig(nil))
			queue.readErr = errRead

			require.ErrorIs(t, read(h), errRead)
		})
	}
}
