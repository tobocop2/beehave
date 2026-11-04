package flow

import (
	"cmp"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
)

// upHosts answers host_up. started is true for a host that this call launched, and false for a host that was already up.
// A teardown is counted and always succeeds.
type upHosts struct {
	mu        sync.Mutex
	started   bool
	err       error
	ups       []string
	asked     []string // the cluster each Up call named
	stopped   []string // the cluster each Teardown call named
	teardowns int
}

// defaultCluster is the build host that upHosts resolves an empty choice to.
const defaultCluster = "default-host"

func (u *upHosts) Up(_ context.Context, project, cluster string) (bool, string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.ups = append(u.ups, project)
	u.asked = append(u.asked, cluster)

	return u.started, cmp.Or(cluster, defaultCluster), u.err
}

func (u *upHosts) Teardown(_ context.Context, _, cluster string) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.teardowns++
	u.stopped = append(u.stopped, cluster)

	return nil
}

const chosenHostFlow = "def main(item):\n    up = host_up(name = \"start\", host = \"gpu-b\")\n    if up.host != \"gpu-b\":\n        fail(\"HOST \" + up.host)\n    fail(\"main failed\")\n\ndef finish(item):\n    host_teardown(name = \"stop\")\n"

func TestHostUp_ARunStartsAndStopsTheHostThatItNamed(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	own := &upHosts{started: true}
	h.hosts = own
	h.flowIs(t, chosenHostFlow)

	_, err := h.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "main failed", "host_up returns the host that the run named")
	assert.Equal(t, []string{"gpu-b"}, own.asked)
	assert.Equal(t, []string{"gpu-b"}, own.stopped, "the stop follows the choice that host_up recorded, with no host argument of its own")
}

func TestHostUp_ARunThatNamesNoHostGetsTheDefaultAndRecordsIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	own := &upHosts{started: true}
	h.hosts = own
	h.flowIs(t, "def main(item):\n    up = host_up(name = \"start\")\n    fail(\"HOST \" + up.host)\n")

	_, err := h.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "HOST "+defaultCluster, "the result holds the resolved host, not the empty choice")
	assert.Equal(t, []string{""}, own.asked)
}

const chosenHostWorkFlow = "def main(item):\n    host_up(name = \"start\", host = \"gpu-b\")\n    gate(name = \"check\")\n    command(name = \"build\", run = [\"make\", \"x\"], host = \"build\")\n"

func TestHostUp_TheGateAndTheCommandsOfARunUseTheHostThatItNamed(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.hosts = &upHosts{started: true}
	h.flowIs(t, chosenHostWorkFlow)

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	hosts := h.gate.gatedOn()
	require.NotEmpty(t, hosts, "the gate ran")

	for _, host := range hosts {
		assert.Equal(t, "gpu-b", host, "every call of the gate names the host that host_up recorded")
	}

	require.Len(t, h.commands.requests, 1)
	assert.Equal(t, "gpu-b", h.commands.requests[0].BuildHost, "the command on the host goes to the host that host_up recorded")
}

func TestHostUp_ARunThatNamedNoHostGatesAndRunsOnTheDefault(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.hosts = &upHosts{started: true}
	h.flowIs(t, "def main(item):\n    host_up(name = \"start\")\n    gate(name = \"check\")\n    command(name = \"build\", run = [\"make\", \"x\"], host = \"build\")\n")

	_, err := h.engine(t).Run(testItem, testProject)
	require.NoError(t, err)

	for _, host := range h.gate.gatedOn() {
		assert.Equal(t, defaultCluster, host, "the recorded host is the default that host_up resolved")
	}

	assert.Equal(t, defaultCluster, h.commands.requests[0].BuildHost)
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
