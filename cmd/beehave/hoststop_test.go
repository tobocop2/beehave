package main

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
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
	started, _, err := stop.Up(context.Background(), "p", "")

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
	project := model.Project{Name: "p", BuildHost: "gpu", BuildHosts: []string{"gpu", "gpu-b"}, Host: &model.HostConfig{}}

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

	started, chosen, err := stop.Up(t.Context(), "p", "local")
	require.NoError(t, err, "host_up(host = \"local\") works on a project that builds here")
	assert.False(t, started)
	assert.Equal(t, "local", chosen)

	_, _, err = stop.Up(t.Context(), "p", "gpu")
	require.ErrorIs(t, err, model.ErrUndeclaredHost, "a cluster name is still refused there")
}

func TestHostStop_UpStartsTheClusterThatTheRunNamedAndReportsIt(t *testing.T) {
	t.Parallel()

	var built []string

	absent := &startingHost{state: model.HostAbsent}
	started, chosen, err := clusters(&built, absent).Up(t.Context(), "p", "gpu-b")

	require.NoError(t, err)
	assert.True(t, started)
	assert.Equal(t, "gpu-b", chosen)
	assert.Equal(t, []string{"gpu-b"}, built, "the host of the chosen cluster is built, not the build host of the project")

	built = nil
	_, chosen, err = clusters(&built, absent).Up(t.Context(), "p", "")

	require.NoError(t, err)
	assert.Equal(t, "gpu", chosen, "a run that names no host gets the build host of the project")
	assert.Equal(t, []string{"gpu"}, built)
}

func TestHostStop_RefusesAClusterThatTheProjectDoesNotDeclare(t *testing.T) {
	t.Parallel()

	var built []string

	absent := &startingHost{state: model.HostAbsent}
	_, _, err := clusters(&built, absent).Up(t.Context(), "p", "rogue")

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
