package flow

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
	"github.com/tobocop2/obee/internal/review"
)

// hostGate is a gate with a host job. It counts its submits and keeps the job of each wait. Its first wait blocks until the run is canceled,
// as a wait on a host job does when the run stops after the submit, and every later wait passes.
type hostGate struct {
	submits *atomic.Int32
	started chan struct{}
	mu      *sync.Mutex
	jobs    *[]GateJob
	// hosts are the build hosts that Submit and Run were given, in order.
	hosts *[]string
	// blockAt is the number of the wait that blocks until the run is canceled.
	blockAt int
}

func newHostGate() hostGate {
	return hostGate{submits: &atomic.Int32{}, started: make(chan struct{}, 1), mu: &sync.Mutex{}, jobs: &[]GateJob{}, hosts: &[]string{}, blockAt: 1}
}

func (g hostGate) gatedOn() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), *g.hosts...)
}

func (hostGate) Key(context.Context, string, string, string) (GateKey, error) { return GateKey{}, nil }

func (g hostGate) Submit(_ context.Context, _, _, host string) (GateJob, error) {
	g.mu.Lock()
	*g.hosts = append(*g.hosts, host)
	g.mu.Unlock()

	return GateJob{ID: "job-" + string(rune('0'+g.submits.Add(1))), Head: gatedHead}, nil
}

func (g hostGate) Run(ctx context.Context, _, _, host string, job GateJob) (GateResult, error) {
	g.mu.Lock()
	*g.hosts = append(*g.hosts, host)
	*g.jobs = append(*g.jobs, job)
	first := len(*g.jobs) == g.blockAt
	g.mu.Unlock()

	if first {
		g.started <- struct{}{}
		<-ctx.Done()

		return GateResult{}, ctx.Err()
	}

	return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
}

func (g hostGate) waitedOn() []GateJob {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]GateJob(nil), *g.jobs...)
}

func TestGateJob_ACrashAfterTheSubmitAndBeforeTheWaitIsRecordedSubmitsOnceAndTheReplayWaitsOnThatJob(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	gated := newHostGate()
	deps := h.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	file := writeFlow(t, bodyFlow("gate(name = \"check\")\n"))
	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	<-gated.started

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	select {
	case runErr := <-ended:
		require.Error(t, runErr)
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not end after the cancel")
	}

	require.EqualValues(t, 1, gated.submits.Load(), "the first run submitted the host job")
	_, err = resumeAndJoin(t, engine, client)

	require.NoError(t, err)
	assert.EqualValues(t, 1, gated.submits.Load(), "the replay submitted no second job")
	assert.Equal(t, []GateJob{{ID: "job-1", Head: gatedHead}, {ID: "job-1", Head: gatedHead}}, gated.waitedOn(), "both waits are on the one recorded job")
}

func TestGateJob_AResumedRunGatesOnTheHostThatItRecordedAndStartsNoHostAgain(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	own := &upHosts{started: true}
	h.hosts = own
	gated := newHostGate()
	deps := h.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	file := writeFlow(t, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\ngate(name = \"check\")\n"))
	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	<-gated.started

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	select {
	case runErr := <-ended:
		require.Error(t, runErr)
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not end after the cancel")
	}

	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err)

	assert.Equal(t, []string{"gpu-b"}, own.asked, "the resume read the recorded host and started none")

	hosts := gated.gatedOn()
	require.GreaterOrEqual(t, len(hosts), 2, "the gate ran before and after the resume")

	for _, host := range hosts {
		assert.Equal(t, "gpu-b", host, "the resumed run gates on the host that the first run recorded")
	}
}

// The cancel falls after the first gate has finished, so the resumed main replays host_up and the first gate from their records and runs
// every later use of the host itself: a second gate, a command and host_teardown. Each one takes the host from the replayed record of host_up.
func TestGateJob_AResumedMainUsesTheHostThatTheReplayedRecordNamesForEveryLaterStep(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	own := &upHosts{started: true}
	h.hosts = own
	gated := newHostGate()
	gated.blockAt = 2
	deps := h.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	file := writeFlow(t, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\ngate(name = \"one\")\ngate(name = \"two\")\ncommand(name = \"build\", run = [\"make\", \"x\"], host = \"build\")\nhost_teardown(name = \"stop\")\n"))
	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file)
		ended <- runErr
	}()

	<-gated.started

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	select {
	case runErr := <-ended:
		require.Error(t, runErr)
	case <-time.After(20 * time.Second):
		t.Fatal("the run did not end after the cancel")
	}

	before := len(gated.gatedOn())

	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err)

	assert.Equal(t, []string{"gpu-b"}, own.asked, "the resume started no host")
	assert.Greater(t, len(gated.gatedOn()), before, "the second gate ran again after the resume")

	for _, host := range gated.gatedOn() {
		assert.Equal(t, "gpu-b", host, "the second gate takes the host from the replayed record")
	}

	require.Len(t, h.commands.requests, 1)
	assert.Equal(t, "gpu-b", h.commands.requests[0].BuildHost, "the command after the resume runs on the recorded host")
	assert.Equal(t, []string{"gpu-b"}, own.stopped, "the stop after the resume stops the recorded host")
}

// gateChildSteps returns the id of the exclusive run that holds the gate, and the names of its steps in order.
func gateChildSteps(t *testing.T, engine *Engine) (string, []string) {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName))
	require.NoError(t, err)
	require.Len(t, rows, 1, "one gate ran")

	steps, err := dbos.GetWorkflowSteps(engine.ctx, rows[0].ID)
	require.NoError(t, err)

	names := make([]string, 0, len(steps))
	for _, step := range steps {
		names = append(names, step.StepName)
	}

	return rows[0].ID, names
}

// The gate workflow itself is replayed past its two finished steps: a fork copies the records of both and runs the workflow function again.
// A gate:job that is not a recorded step submits again here, and a gate step that is not recorded waits again.
func TestGateJob_AGateWorkflowReplayedPastItsFinishedStepsSubmitsAndWaitsOnNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: gatedHead}
	engine := h.engine(t)

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("gate(name = \"check\")\n")))
	require.NoError(t, err)

	id, names := gateChildSteps(t, engine)
	require.Equal(t, []string{gateJobName, kindGate}, names, "the gate is two recorded steps")
	require.EqualValues(t, 1, h.gate.submits.Load())
	require.Len(t, h.gate.waitedOn(), 1)

	forked, err := dbos.ForkWorkflow[ExclusiveOutput](engine.ctx, dbos.ForkWorkflowInput{OriginalWorkflowID: id, StartStep: uint(len(names))})
	require.NoError(t, err)

	out, err := forked.GetResult()

	require.NoError(t, err)
	assert.Nil(t, out.Failure)
	assert.EqualValues(t, 1, h.gate.submits.Load(), "the replay submitted no second job")
	assert.Len(t, h.gate.waitedOn(), 1, "the replay waited on nothing")
}
