package flow

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

// upHosts answers host_up. started is true for a host that this call launched, and false for a host that was already up.
// A teardown is counted and always succeeds. LostProof answers from proofs in order, the last repeating; with none it gives no proof.
type upHosts struct {
	mu      sync.Mutex
	started bool
	err     error
	ups     []string
	asked   []string // the cluster each Up call named
	// readQueue makes Up read the gates that wait on the cluster, as a host with a size table does. waiting and waitErr hold the answer.
	readQueue bool
	waiting   int
	waitErr   error
	stopped   []string // the cluster each Teardown call named
	teardowns int
	proofs    []model.LostProof
	proofErr  error
	// proved holds the cluster and the job of each LostProof call, as "cluster/job".
	proved []string
	// afterProof runs after each LostProof call, outside the lock, when a test sets it.
	afterProof func()
}

// defaultCluster is the build host that upHosts resolves an empty choice to.
const defaultCluster = "default-host"

func (u *upHosts) Up(_ context.Context, project, cluster string, waiting func(string) (int, error)) (bool, string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.ups = append(u.ups, project)
	u.asked = append(u.asked, cluster)

	if u.readQueue {
		u.waiting, u.waitErr = waiting(cmp.Or(cluster, defaultCluster))
	}

	return u.started, cmp.Or(cluster, defaultCluster), u.err
}

// Sized says no host has a size table, and Defer says no host is left for a gate.
func (*upHosts) Sized(string, string) (bool, error) { return false, nil }

func (*upHosts) Defer(_ context.Context, _, cluster string) (bool, string, error) {
	return false, cmp.Or(cluster, defaultCluster), nil
}

func (u *upHosts) Teardown(_ context.Context, _, cluster string) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.teardowns++
	u.stopped = append(u.stopped, cluster)

	return nil
}

// Ready says every host is ready.
func (*upHosts) Ready(context.Context, string, string) (bool, error) { return true, nil }

// CheckCredentials passes every host.
func (*upHosts) CheckCredentials(context.Context, string) error { return nil }

func (u *upHosts) LostProof(_ context.Context, _, cluster, job string) (model.LostProof, error) {
	u.mu.Lock()

	var proof model.LostProof
	if len(u.proofs) > 0 {
		proof = u.proofs[min(len(u.proved), len(u.proofs)-1)]
	}

	u.proved = append(u.proved, cluster+"/"+job)
	after, err := u.afterProof, u.proofErr
	u.mu.Unlock()

	if after != nil {
		after()
	}

	return proof, err
}

// proofReads counts the LostProof calls so far.
func (u *upHosts) proofReads() int {
	u.mu.Lock()
	defer u.mu.Unlock()

	return len(u.proved)
}

// prove makes every later LostProof call give proof.
func (u *upHosts) prove(proof model.LostProof) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.proofs = []model.LostProof{proof}
}

// teardownCount counts the Teardown calls so far.
func (u *upHosts) teardownCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()

	return u.teardowns
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

func TestHostUp_ThePassedQueueReadCountsTheGatesOnTheClusterFromTheGateQueue(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	own := &upHosts{started: true, readQueue: true}
	h.hosts = own
	h.flowIs(t, chosenHostFlow)

	_, err := h.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "main failed")
	require.NoError(t, own.waitErr)
	assert.Zero(t, own.waiting, "no gate waits on gpu-b in a store with no gate")
}

func TestGatesWaiting_CountsTheGatesOfOneClusterOfEveryProject(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).opened(t)
	rows := append(slices.Repeat([]string{"gpu"}, 12), "gpu-b", "gpu-b")

	for project, want := range map[string]int{"demo": 12, "other": 12} {
		got, err := engine.gatesWaiting(project, jobsOn(rows...))("gpu")

		require.NoError(t, err, project)
		assert.Equal(t, want, got, project+": twelve gates on gpu, none of gpu-b")
	}

	got, err := engine.gatesWaiting("demo", jobsOn(rows...))("gpu-b")
	require.NoError(t, err)
	assert.Equal(t, 2, got)
}

func TestHostUp_ThePassedQueueReadRefusesWhenTheHostNamesNoGateSlots(t *testing.T) {
	t.Parallel()

	refused := errors.New("the host block names no gate slots")
	h := newHarness(t)
	own := &upHosts{started: true, readQueue: true}
	h.hosts = own
	h.slots = func(string, string, string) (SlotGrant, error) { return SlotGrant{}, refused }
	h.flowIs(t, chosenHostFlow)

	_, err := h.engine(t).Run(testItem, testProject)

	require.ErrorContains(t, err, "main failed")
	require.ErrorIs(t, own.waitErr, refused)
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
