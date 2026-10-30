package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host"
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
