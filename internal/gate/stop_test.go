package gate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/gate"
	"github.com/tobocop2/obee/internal/model"
)

func endedContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return ctx
}

func TestRun_AHostGateWhoseContextEndsStopsItsHostJobWithAContextThatStillWorks(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{stopOnContext: true}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(endedContext(), hostRequest(hostProject()))

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []model.JobID{"job-7"}, remote.canceled)
	require.NoError(t, remote.cancelCtxErr, "the stop must not run under the context that ended")
}

func TestRun_AHostJobThatCannotBeStoppedIsNamedInTheError(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{stopOnContext: true, cancelErr: errors.New("ssh down")}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(endedContext(), hostRequest(hostProject()))

	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "job-7")
	require.ErrorContains(t, err, "ssh down")
}

func TestRun_AHostGateThatEndsOnItsOwnStopsNoJob(t *testing.T) {
	t.Parallel()

	remote := &fakeRemote{state: model.JobSucceeded, stopOnContext: true}
	g := build(&fakeShell{}, &fakeTree{states: []gate.State{clean(headA)}}, remote)

	_, err := g.Run(context.Background(), hostRequest(hostProject()))

	require.NoError(t, err)
	assert.Empty(t, remote.canceled)
}
