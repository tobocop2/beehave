package main

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/hosttest"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/host/sshtest"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
)

type tearingHost struct {
	host.Host

	err   error
	calls int
}

func (h *tearingHost) Teardown(context.Context) error {
	h.calls++

	return h.err
}

// stopOf is a hostStop whose project has the host that returns h and err for every cluster.
func stopOf(h host.Host, err error) hostStop {
	return hostStop{programs: programs{hosts: func(string, string) (host.Host, error) { return h, err }}}
}

// upOf starts the host of project p and gives back whether the call launched it and the error.
func upOf(stop hostStop) (bool, error) {
	started, _, err := stop.Up(context.Background(), "p", "", noWaiting)

	return started, err
}

func TestHostStop_StopsTheHostOfTheProjectAndPassesOnItsRefusal(t *testing.T) {
	t.Parallel()

	refusal := errors.New("host stays up: work is not on a remote")
	hosted := &tearingHost{err: refusal}
	stop := stopOf(hosted, nil)

	require.ErrorIs(t, stop.Teardown(t.Context(), "p", ""), refusal)

	hosted.err = nil
	require.NoError(t, stop.Teardown(t.Context(), "p", ""))
	assert.Equal(t, 2, hosted.calls)
}

type startingHost struct {
	host.Host

	state     model.HostState
	statusErr error
	upErr     error
	ups       int
}

func (h *startingHost) Status(context.Context) (model.HostStatus, error) {
	return model.HostStatus{State: h.state}, h.statusErr
}

func (h *startingHost) Up(context.Context) error {
	h.ups++

	return h.upErr
}

// noWaiting says no gate waits on any cluster.
func noWaiting(string) (int, error) { return 0, nil }

// sizedStartingHost is a startingHost whose size follows the queue. It records the queue length that it started with.
type sizedStartingHost struct {
	startingHost

	queued []int
}

func (*sizedStartingHost) HasSizeTable() bool { return true }

func (h *sizedStartingHost) UpQueued(_ context.Context, queued int) error {
	h.queued = append(h.queued, queued)

	return h.upErr
}

func TestHostStop_UpStartsAHostWithASizeTableWithTheGatesThatWaitOnItsCluster(t *testing.T) {
	t.Parallel()

	sized := &sizedStartingHost{startingHost: startingHost{state: model.HostAbsent}}
	asked := ""

	started, _, err := stopOf(sized, nil).Up(t.Context(), "p", "gpu", func(cluster string) (int, error) {
		asked = cluster

		return 12, nil
	})

	require.NoError(t, err)
	assert.True(t, started)
	assert.Equal(t, []int{12}, sized.queued)
	assert.Equal(t, "gpu", asked)
	assert.Zero(t, sized.ups, "the plain start is not used")
}

func TestHostStop_UpReadsNoQueueForAHostWithNoSizeTableOrAHostThatRuns(t *testing.T) {
	t.Parallel()

	plain := &startingHost{state: model.HostAbsent}
	sized := &sizedStartingHost{startingHost: startingHost{state: model.HostUp}}
	forbidden := func(string) (int, error) { return 0, errors.New("the queue is read") }

	_, _, err := stopOf(plain, nil).Up(t.Context(), "p", "", forbidden)
	require.NoError(t, err)
	assert.Equal(t, 1, plain.ups)

	started, _, err := stopOf(sized, nil).Up(t.Context(), "p", "", forbidden)
	require.NoError(t, err)
	assert.False(t, started, "a host that runs keeps its size")
	assert.Empty(t, sized.queued)
}

func TestHostStop_UpRefusesToStartASizedHostWhenTheQueueCannotBeRead(t *testing.T) {
	t.Parallel()

	sized := &sizedStartingHost{startingHost: startingHost{state: model.HostAbsent}}
	unread := errors.New("the store does not answer")

	_, _, err := stopOf(sized, nil).Up(t.Context(), "p", "", func(string) (int, error) { return 0, unread })

	require.ErrorIs(t, err, unread)
	assert.Empty(t, sized.queued, "no host starts at a guessed size")
}

func TestHostStop_UpStartsAnAbsentHostAndReusesALiveOne(t *testing.T) {
	t.Parallel()

	absent := &startingHost{state: model.HostAbsent}
	started, err := upOf(stopOf(absent, nil))
	require.NoError(t, err)
	assert.True(t, started)
	assert.Equal(t, 1, absent.ups, "a host that is not there is launched")

	live := &startingHost{state: model.HostUp}
	started, err = upOf(stopOf(live, nil))
	require.NoError(t, err)
	assert.False(t, started)
	assert.Zero(t, live.ups, "a live host is not launched again")
}

func TestHostStop_UpLaunchesOnlyAnAbsentHostAndNamesTheStateOfAnyOther(t *testing.T) {
	t.Parallel()

	for _, state := range []model.HostState{model.HostDraining, "stopped", "init", ""} {
		other := &startingHost{state: state}
		_, err := upOf(stopOf(other, nil))

		require.ErrorIs(t, err, errHostState, string(state))
		require.ErrorContains(t, err, "the host is "+strconv.Quote(string(state)))
		assert.Zero(t, other.ups, "a host in state %q is not launched over", state)
	}
}

func TestHostStop_UpRefusesAReadThatFailsAndStartsNothingForAProjectWithNoHost(t *testing.T) {
	t.Parallel()

	unreadable := &startingHost{statusErr: errors.New("sky status failed")}
	_, err := upOf(stopOf(unreadable, nil))
	require.ErrorContains(t, err, "sky status failed")
	assert.Zero(t, unreadable.ups, "an unreadable host is not launched over")

	failing := &startingHost{state: model.HostAbsent, upErr: errors.New("launch refused")}
	_, err = upOf(stopOf(failing, nil))
	require.ErrorContains(t, err, "launch refused")

	started, err := upOf(stopOf(nil, errNoHost))
	require.NoError(t, err, "a project with no host block has nothing to start")
	assert.False(t, started)

	broken := errors.New("host config is unreadable")
	_, err = upOf(stopOf(nil, broken))
	require.ErrorIs(t, err, broken)
}

func TestHostStop_SizedIsTrueOnlyForAProviderWithASizeTable(t *testing.T) {
	t.Parallel()

	var built []string

	sized, err := clusters(&built, &sizedStartingHost{}).Sized("p", "gpu-b")
	require.NoError(t, err)
	assert.True(t, sized)
	assert.Equal(t, []string{"gpu-b"}, built, "the cluster of the run is the one that is built")

	sized, err = stopOf(&startingHost{}, nil).Sized("p", "")
	require.NoError(t, err)
	assert.False(t, sized, "a provider with no size table")

	sized, err = stopOf(nil, errNoHost).Sized("p", "")
	require.NoError(t, err)
	assert.False(t, sized, "a project with no host block")

	broken := errors.New("host config is unreadable")
	_, err = stopOf(nil, broken).Sized("p", "")
	require.ErrorIs(t, err, broken)

	_, err = clusters(&built, &sizedStartingHost{}).Sized("p", "rogue")
	require.Error(t, err, "a cluster that the project does not declare")

	_, err = clusters(&built, &sizedStartingHost{}).Sized("other", "")
	require.ErrorIs(t, err, errUnknownProject)
}

func TestHostStop_DeferLeavesOnlyAnAbsentHostWithASizeTableForTheFirstGate(t *testing.T) {
	t.Parallel()

	var built []string

	deferred, chosen, err := clusters(&built, &sizedStartingHost{startingHost: startingHost{state: model.HostAbsent}}).Defer(t.Context(), "p", "gpu-b")
	require.NoError(t, err)
	assert.True(t, deferred)
	assert.Equal(t, "gpu-b", chosen)

	for name, h := range map[string]host.Host{
		"a host that is up":        &sizedStartingHost{startingHost: startingHost{state: model.HostUp}},
		"a provider with no table": &startingHost{state: model.HostAbsent},
	} {
		deferred, _, err = stopOf(h, nil).Defer(t.Context(), "p", "")
		require.NoError(t, err, name)
		assert.False(t, deferred, name)
	}

	deferred, _, err = stopOf(nil, errNoHost).Defer(t.Context(), "p", "")
	require.NoError(t, err)
	assert.False(t, deferred, "a project with no host block")
}

func TestHostStop_DeferPassesOnEveryFaultThatItCannotSettle(t *testing.T) {
	t.Parallel()

	var built []string

	_, _, err := clusters(&built, &sizedStartingHost{}).Defer(t.Context(), "p", "rogue")
	require.Error(t, err, "a cluster that the project does not declare")

	broken := errors.New("host config is unreadable")
	_, _, err = stopOf(nil, broken).Defer(t.Context(), "p", "")
	require.ErrorIs(t, err, broken)

	unreadable := &sizedStartingHost{startingHost: startingHost{statusErr: errors.New("sky status failed")}}
	_, _, err = stopOf(unreadable, nil).Defer(t.Context(), "p", "")
	require.ErrorContains(t, err, "sky status failed")
}

// skyPilotStop loads a project whose skypilot host block holds the keys, and builds its real SkyPilot host over a scripted sky that lists no cluster.
func skyPilotStop(t *testing.T, keys string) (hostStop, *proctest.Scripted) {
	t.Helper()

	project, problems := loadHostProject(t, hostProjectTail+`, host = host(provider = "skypilot", disk_floor_gb = 50, idle_minutes = 30`+keys+`)`)
	require.Empty(t, problems)

	sky, sshRunner := &proctest.Scripted{}, &proctest.Scripted{}
	sky.Plan("[]", "status")
	sky.Plan("", "launch")
	sshRunner.Plan("flock from util-linux 2.39.3\n", "flock", "--version")

	h, err := skypilot.New(project.BuildHost, *project.Host, sky, sshRunner, slurmQueue)
	require.NoError(t, err)

	return stopOf(h, nil), sky
}

func TestHostStop_OnlyAnInstanceSizesTableLeavesAnAbsentSkyPilotHostForTheFirstGate(t *testing.T) {
	t.Parallel()

	unread := func(string) (int, error) { return 0, errors.New("the queue is read for a host with no size table") }

	for name, tc := range map[string]struct {
		keys     string
		deferred bool
		launch   []string
	}{
		"instance_type_alone": {`, skypilot = skypilot(instance_type = "n2-standard-8")`, false, []string{"-t", "n2-standard-8"}},
		"no_size_key":         {`, skypilot = skypilot(cloud = "gcp")`, false, []string{"--infra", "gcp"}},
		"no_block":            {"", false, []string{"--down"}},
		"instance_sizes":      {`, skypilot = skypilot(instance_sizes = "3:n2-standard-4,n2-standard-16")`, true, nil},
	} {
		stop, sky := skyPilotStop(t, tc.keys)

		sized, err := stop.Sized("p", "")
		require.NoError(t, err, name)
		assert.Equal(t, tc.deferred, sized, name+": only a host with an instance_sizes table is sized")

		deferred, _, err := stop.Defer(t.Context(), "p", "")
		require.NoError(t, err, name)
		assert.Equal(t, tc.deferred, deferred, name+": host_up records a deferral only for a host with an instance_sizes table")

		if tc.deferred {
			assert.False(t, sky.Called("launch"), name)

			continue
		}

		started, _, err := stop.Up(t.Context(), "p", "", unread)
		require.NoError(t, err, name)
		assert.True(t, started, name+": host_up starts the host as at the base")
		assert.True(t, sky.Called(tc.launch...), name)
	}
}

func TestHostStop_PassesOnAHostLookupErrorThatIsNotNoHost(t *testing.T) {
	t.Parallel()

	broken := errors.New("host config is unreadable")

	require.ErrorIs(t, stopOf(nil, broken).Teardown(t.Context(), "p", ""), broken)
}

func TestHostStop_RefusesAProjectWithNoHostAndAProviderWithNoTeardown(t *testing.T) {
	t.Parallel()

	require.EqualError(t, stopOf(nil, errNoHost).Teardown(t.Context(), "p", ""), "project p has no host, so there is no host to stop")
	require.ErrorIs(t, stopOf(&fakeHost{}, nil).Teardown(t.Context(), "p", ""), errNoTeardown)
}

// clusters is a hostStop whose project declares gpu and gpu-b. Every host it builds records the cluster it was built for.
func clusters(built *[]string, h host.Host) hostStop {
	project := model.Project{Name: "p", BuildHost: "gpu", BuildHosts: []string{"gpu", "gpu-b"}, Host: loaderHost()}

	return hostStop{programs: programs{
		declared: func(name, cluster string) (string, error) {
			if name != "p" {
				return "", errUnknownProject
			}

			return project.HostFor(cluster)
		},
		hosts: func(_, cluster string) (host.Host, error) {
			*built = append(*built, cluster)

			return h, nil
		},
	}}
}

func TestHostStop_AWorkflowThatNamesTheLocalHostStartsNothingOnAProjectThatBuildsHere(t *testing.T) {
	t.Parallel()

	local := model.Project{Name: "p", BuildHost: "local"}
	stop := hostStop{programs: programs{
		declared: func(_, cluster string) (string, error) { return local.HostFor(cluster) },
		hosts:    func(_, _ string) (host.Host, error) { return nil, errNoHost },
	}}

	started, chosen, err := stop.Up(t.Context(), "p", "local", noWaiting)
	require.NoError(t, err, "host_up(host = \"local\") works on a project that builds here")
	assert.False(t, started)
	assert.Equal(t, "local", chosen)

	_, _, err = stop.Up(t.Context(), "p", "gpu", noWaiting)
	require.ErrorIs(t, err, model.ErrUndeclaredHost, "a cluster name is still refused there")
}

func TestHostStop_UpStartsTheClusterThatTheRunNamedAndReportsIt(t *testing.T) {
	t.Parallel()

	var built []string

	absent := &startingHost{state: model.HostAbsent}
	started, chosen, err := clusters(&built, absent).Up(t.Context(), "p", "gpu-b", noWaiting)

	require.NoError(t, err)
	assert.True(t, started)
	assert.Equal(t, "gpu-b", chosen)
	assert.Equal(t, []string{"gpu-b"}, built, "the host of the chosen cluster is built, not the build host of the project")

	built = nil
	_, chosen, err = clusters(&built, absent).Up(t.Context(), "p", "", noWaiting)

	require.NoError(t, err)
	assert.Equal(t, "gpu", chosen, "a run that names no host gets the build host of the project")
	assert.Equal(t, []string{"gpu"}, built)
}

func TestHostStop_RefusesAClusterThatTheProjectDoesNotDeclare(t *testing.T) {
	t.Parallel()

	var built []string

	absent := &startingHost{state: model.HostAbsent}
	_, _, err := clusters(&built, absent).Up(t.Context(), "p", "rogue", noWaiting)

	require.ErrorIs(t, err, model.ErrUndeclaredHost)
	assert.Empty(t, built)
	assert.Zero(t, absent.ups, "no machine starts for a host that no project file declares")

	err = clusters(&built, &tearingHost{}).Teardown(t.Context(), "p", "rogue")

	require.ErrorIs(t, err, model.ErrUndeclaredHost)
	assert.Empty(t, built)
}

func TestHostStop_StopsTheClusterThatTheRunChose(t *testing.T) {
	t.Parallel()

	var built []string

	tearing := &tearingHost{}

	require.NoError(t, clusters(&built, tearing).Teardown(t.Context(), "p", "gpu-b"))
	assert.Equal(t, []string{"gpu-b"}, built)
	assert.Equal(t, 1, tearing.calls)
}

// sshAt is the config of an ssh host whose work root is on the fake machine.
func sshAt() model.HostConfig {
	return model.HostConfig{
		Provider: ssh.Name, MaxRun: 2 * time.Hour, StaleMinutes: 20, PrivateRemote: "private",
		Block: model.NewBlock("test", []model.BlockEntry{
			model.TextEntry(ssh.BlockUser, "lane"), model.TextEntry(ssh.BlockWorkRoot, sshtest.Root+"/work"), model.TextEntry(ssh.BlockKnownHosts, model.KnownHostsStrict),
		}),
	}
}

func TestHostStop_TheLostProofReadsTheSshHostThatTheJobRanOn(t *testing.T) {
	t.Parallel()

	first := sshtest.New(hosttest.Working)
	machine := first

	var built []string

	stop := hostStop{programs: programs{hosts: func(_, cluster string) (host.Host, error) {
		built = append(built, cluster)

		return ssh.New("gates", sshAt(), machine.SSH())
	}}}

	ran, err := ssh.New("gates", sshAt(), first.SSH())
	require.NoError(t, err)

	id, err := ran.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "make"})
	require.NoError(t, err)

	first.Drops = -1

	proof, err := stop.LostProof(t.Context(), "p", "gpu-b", string(id))

	require.NoError(t, err, "a link that is down is no proof and no error")
	assert.Empty(t, proof.Kind)
	assert.NotEmpty(t, proof.Note, "the note says why there is no proof")

	// A new machine answers at the alias of the host. It has no state folder.
	machine = sshtest.New(hosttest.Working)
	machine.StrictFolders = true

	proof, err = stop.LostProof(t.Context(), "p", "gpu-b", string(id))

	require.NoError(t, err)
	assert.Equal(t, model.LostStopped, proof.Kind, "the stop of a unit that the machine does not hold answers")
	assert.True(t, machine.Files[sshtest.Root+"/work/.beehave/"], "the stop made the state folder")
	assert.Equal(t, []string{"gpu-b", "gpu-b"}, built, "each read builds the host of the cluster that the job ran on")
}

func TestHostStop_TheLostProofOfAProjectWithNoHostIsNoProofAndAnyOtherFaultIsAnError(t *testing.T) {
	t.Parallel()

	proof, err := stopOf(nil, errNoHost).LostProof(t.Context(), "p", "", "job-1")
	require.NoError(t, err, "a project with no host has no job that can run")
	assert.Empty(t, proof.Kind)

	broken := errors.New("host config is unreadable")
	_, err = stopOf(nil, broken).LostProof(t.Context(), "p", "", "job-1")
	require.ErrorIs(t, err, broken)

	var built []string

	_, err = clusters(&built, &fakeHost{}).LostProof(t.Context(), "p", "rogue", "job-1")
	require.ErrorIs(t, err, model.ErrUndeclaredHost)
	assert.Empty(t, built, "no host is built for a cluster that the project does not declare")
}

// answeringHost is a host.Host that answers Status from its fields.
type answeringHost struct {
	host.Host

	status model.HostStatus
	err    error
}

func (s *answeringHost) Status(context.Context) (model.HostStatus, error) { return s.status, s.err }

func TestHostStop_ReadyIsTrueOnlyForAnUpHostWithEnoughSpace(t *testing.T) {
	t.Parallel()

	const gb = 1 << 30

	roomy := model.DiskReading{FreeBytes: 60 * gb, Readable: true}
	tests := []struct {
		name   string
		status model.HostStatus
		want   bool
	}{
		{"up", model.HostStatus{State: model.HostUp, Disk: roomy}, true},
		{"absent", model.HostStatus{State: model.HostAbsent, Disk: roomy}, false},
		{"draining", model.HostStatus{State: model.HostDraining, Disk: roomy}, false},
		{"under_the_floor", model.HostStatus{State: model.HostUp, Disk: model.DiskReading{FreeBytes: 10 * gb, Readable: true}}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ready, err := stopOf(host.WithDiskFloor(&answeringHost{status: tc.status}, 50), nil).Ready(t.Context(), "p", "")
			require.NoError(t, err)
			assert.Equal(t, tc.want, ready)
		})
	}
}

func TestHostStop_ReadyNamesTheHostWhenItCannotBeRead(t *testing.T) {
	t.Parallel()

	var built []string

	_, err := clusters(&built, host.WithDiskFloor(&answeringHost{err: errFake}, 50)).Ready(t.Context(), "p", "gpu-b")
	require.ErrorIs(t, err, errFake)
	assert.Contains(t, err.Error(), "cannot read host gpu-b of project p: check that it answers")
	assert.Equal(t, []string{"gpu-b"}, built, "the host of the asked cluster is read")
}

func TestHostStop_ReadyOfAProjectWithNoHostIsFalseAndAnyOtherFaultIsAnError(t *testing.T) {
	t.Parallel()

	ready, err := stopOf(nil, errNoHost).Ready(t.Context(), "p", "")
	require.NoError(t, err)
	assert.False(t, ready, "a project with no host has no host that takes a job")

	broken := errors.New("host config is unreadable")
	_, err = stopOf(nil, broken).Ready(t.Context(), "p", "")
	require.ErrorIs(t, err, broken)

	var built []string

	_, err = clusters(&built, &answeringHost{}).Ready(t.Context(), "p", "rogue")
	require.ErrorIs(t, err, model.ErrUndeclaredHost)
	assert.Empty(t, built, "no host is built for a cluster that the project does not declare")
}
