package flow

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// upHosts answers host_up. started is true for a host that this call launched, and false for a host that was already up.
// A teardown is counted and always succeeds.
type upHosts struct {
	mu        sync.Mutex
	started   bool
	err       error
	ups       []string
	teardowns int
}

func (u *upHosts) Up(_ context.Context, project string) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.ups = append(u.ups, project)

	return u.started, u.err
}

func (u *upHosts) Teardown(context.Context, string) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.teardowns++

	return nil
}

const hostUpFlow = "def main(item):\n    up = host_up(name = \"start\")\n    if up.started:\n        fail(\"STARTED\")\n"

func TestHostUp_TheEngineStartsTheHostOfTheProjectAndTellsWhetherThisCallStartedIt(t *testing.T) {
	t.Parallel()

	reused := newHarness(t)
	live := &upHosts{}
	reused.hosts = live
	reused.flowIs(t, hostUpFlow)

	_, err := reused.engine(t).Run(testItem, testProject)

	require.NoError(t, err, "a host that was already up is not reported as started")
	assert.Equal(t, []string{testProject}, live.ups)

	fresh := newHarness(t)
	fresh.hosts = &upHosts{started: true}
	fresh.flowIs(t, hostUpFlow)

	_, err = fresh.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "STARTED", "a host that this call launched is reported as started")
}

func TestHostUp_AHostThatFailsToStartEndsTheRunWithTheReason(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.hosts = &upHosts{err: errors.New("the cloud refused the launch")}
	h.flowIs(t, hostUpFlow)

	_, err := h.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "the cloud refused the launch")
}

func TestHostUp_ABuildWithNoHostKeeperStartsNothingAndReportsNoStart(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.flowIs(t, hostUpFlow)

	_, err := h.engine(t).Run(testItem, testProject)

	require.NoError(t, err)
}

const hostTearFlow = "def main(item):\n    host_up(name = \"start\")\n    fail(\"main failed\")\n\ndef finish(item):\n    stop = host_teardown(name = \"stop\")\n    if not stop.down:\n        fail(stop.reason)\n"

func TestHostTeardown_ARunStopsOnlyAHostThatItStartedAndAFailedRunStillStopsIt(t *testing.T) {
	t.Parallel()

	started := newHarness(t)
	own := &upHosts{started: true}
	started.hosts = own
	started.flowIs(t, hostTearFlow)

	_, err := started.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "main failed")
	assert.Equal(t, 1, own.teardowns, "a run that started the host stops it, although main failed")

	reused := newHarness(t)
	other := &upHosts{}
	reused.hosts = other
	reused.flowIs(t, hostTearFlow)

	_, err = reused.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "did not start the host")
	assert.Zero(t, other.teardowns, "a run never stops a host that it found live")
}
