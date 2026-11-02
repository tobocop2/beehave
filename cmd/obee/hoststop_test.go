package main

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
	"github.com/tobocop2/obee/internal/model"
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

func TestHostStop_StopsTheHostOfTheProjectAndPassesOnItsRefusal(t *testing.T) {
	t.Parallel()

	refusal := errors.New("host stays up: work is not on a remote")
	hosted := &tearingHost{err: refusal}
	stop := hostStop{programs: programs{hosts: func(string) (host.Host, error) { return hosted, nil }}}

	require.ErrorIs(t, stop.Teardown(t.Context(), "p"), refusal)

	hosted.err = nil
	require.NoError(t, stop.Teardown(t.Context(), "p"))
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
	started, err := hostStop{programs: programs{hosts: func(string) (host.Host, error) { return absent, nil }}}.Up(t.Context(), "p")
	require.NoError(t, err)
	assert.True(t, started)
	assert.Equal(t, 1, absent.ups, "a host that is not there is launched")

	live := &startingHost{state: model.HostUp}
	started, err = hostStop{programs: programs{hosts: func(string) (host.Host, error) { return live, nil }}}.Up(t.Context(), "p")
	require.NoError(t, err)
	assert.False(t, started)
	assert.Zero(t, live.ups, "a live host is not launched again")
}

func TestHostStop_UpLaunchesOnlyAnAbsentHostAndNamesTheStateOfAnyOther(t *testing.T) {
	t.Parallel()

	for _, state := range []model.HostState{model.HostDraining, "stopped", "init", ""} {
		other := &startingHost{state: state}
		_, err := hostStop{programs: programs{hosts: func(string) (host.Host, error) { return other, nil }}}.Up(t.Context(), "p")

		require.ErrorIs(t, err, errHostState, string(state))
		require.ErrorContains(t, err, "the host is "+strconv.Quote(string(state)))
		assert.Zero(t, other.ups, "a host in state %q is not launched over", state)
	}
}

func TestHostStop_UpRefusesAReadThatFailsAndStartsNothingForAProjectWithNoHost(t *testing.T) {
	t.Parallel()

	unreadable := &startingHost{statusErr: errors.New("sky status failed")}
	_, err := hostStop{programs: programs{hosts: func(string) (host.Host, error) { return unreadable, nil }}}.Up(t.Context(), "p")
	require.ErrorContains(t, err, "sky status failed")
	assert.Zero(t, unreadable.ups, "an unreadable host is not launched over")

	failing := &startingHost{state: model.HostAbsent, upErr: errors.New("launch refused")}
	_, err = hostStop{programs: programs{hosts: func(string) (host.Host, error) { return failing, nil }}}.Up(t.Context(), "p")
	require.ErrorContains(t, err, "launch refused")

	started, err := hostStop{programs: programs{hosts: func(string) (host.Host, error) { return nil, errNoHost }}}.Up(t.Context(), "p")
	require.NoError(t, err, "a project with no host block has nothing to start")
	assert.False(t, started)

	broken := errors.New("host config is unreadable")
	_, err = hostStop{programs: programs{hosts: func(string) (host.Host, error) { return nil, broken }}}.Up(t.Context(), "p")
	require.ErrorIs(t, err, broken)
}

func TestHostStop_PassesOnAHostLookupErrorThatIsNotNoHost(t *testing.T) {
	t.Parallel()

	broken := errors.New("host config is unreadable")
	stop := hostStop{programs: programs{hosts: func(string) (host.Host, error) { return nil, broken }}}

	require.ErrorIs(t, stop.Teardown(t.Context(), "p"), broken)
}

func TestHostStop_RefusesAProjectWithNoHostAndAProviderWithNoTeardown(t *testing.T) {
	t.Parallel()

	none := hostStop{programs: programs{hosts: func(string) (host.Host, error) { return nil, errNoHost }}}
	require.EqualError(t, none.Teardown(t.Context(), "p"), "project p has no host, so there is no host to stop")

	plain := hostStop{programs: programs{hosts: func(string) (host.Host, error) { return &fakeHost{}, nil }}}
	require.ErrorIs(t, plain.Teardown(t.Context(), "p"), errNoTeardown)
}
