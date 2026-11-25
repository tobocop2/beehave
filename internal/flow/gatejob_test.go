package flow

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gatepkg "github.com/tobocop2/beehave/internal/gate"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

// hostGate is a gate with a host job. It counts its submits and keeps the job of each wait. Its first wait blocks until the run is canceled,
// as a wait on a host job does when the run stops after the submit, and every later wait passes.
type hostGate struct {
	cleanTree

	submits *atomic.Int32
	started chan struct{}
	mu      *sync.Mutex
	jobs    *[]GateJob
	// hosts are the build hosts that Submit and Run were given, in order.
	hosts *[]string
	// blockAt is the number of the wait that blocks until the run is canceled.
	blockAt int
	// plan is the plan that every Submit gives its job.
	plan gatepkg.Plan
	// hold, when set, keeps the blocked wait from returning after its run is canceled, until the test closes it.
	hold chan struct{}
	// later gets one value when a wait after the blocked one starts. waiting gets one value when the test begins to wait for the blocked wait to return.
	later, waiting chan struct{}
	// returned counts the blocked waits that have returned.
	returned *atomic.Int32
	// events lists, in order, the starts and the returns of the waits.
	events *[]string
}

func newHostGate() hostGate {
	return hostGate{
		submits: &atomic.Int32{}, started: make(chan struct{}, 1), mu: &sync.Mutex{}, jobs: &[]GateJob{}, hosts: &[]string{}, blockAt: 1,
		later: make(chan struct{}, 1), waiting: make(chan struct{}, 1), returned: &atomic.Int32{}, events: &[]string{},
	}
}

// record adds an event of the waits to the list.
func (g hostGate) record(event string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	*g.events = append(*g.events, event)
}

// eventsSeen is the list of the starts and the returns of the waits, in order.
func (g hostGate) eventsSeen() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), *g.events...)
}

func (g hostGate) gatedOn() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([]string(nil), *g.hosts...)
}

func (hostGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (g hostGate) Submit(_ context.Context, _, _, host, _, _, _ string, _ model.Slot) (GateJob, error) {
	g.mu.Lock()
	*g.hosts = append(*g.hosts, host)
	g.mu.Unlock()

	return GateJob{ID: "job-" + string(rune('0'+g.submits.Add(1))), Head: gatedHead, Plan: g.plan}, nil
}

func (g hostGate) Run(ctx context.Context, _, _, host string, job GateJob) (GateResult, error) {
	g.mu.Lock()
	*g.hosts = append(*g.hosts, host)
	*g.jobs = append(*g.jobs, job)
	n := len(*g.jobs)
	first := n == g.blockAt
	*g.events = append(*g.events, fmt.Sprintf("wait %d starts", n))
	g.mu.Unlock()

	if first {
		g.started <- struct{}{}
		hang(ctx.Done())

		if g.hold != nil {
			hang(g.hold)
		}

		g.record(fmt.Sprintf("wait %d returns", n))
		g.returned.Add(1)

		return GateResult{}, ctx.Err()
	}

	if n > g.blockAt {
		select {
		case g.later <- struct{}{}:
		default:
		}
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
		_, runErr := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	receive(t, gated.started)

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	require.Error(t, receive(t, ended))

	require.EqualValues(t, 1, gated.submits.Load(), "the first run submitted the host job")
	_, err = resumeAndJoin(t, engine, client)

	require.NoError(t, err)
	assert.EqualValues(t, 1, gated.submits.Load(), "the replay submitted no second job")
	assert.Equal(t, []GateJob{{ID: "job-1", Head: gatedHead}, {ID: "job-1", Head: gatedHead}}, gated.waitedOn(), "both waits are on the one recorded job")
}

func TestGateJob_ARestartAfterAConfigEditFinishesTheGateWithTheListOfTheJobAndNotTheListOfTheFile(t *testing.T) {
	t.Parallel()

	submitted := []model.GateSpec{{Name: "fmt", Command: "make fmt"}, {Name: "lint", Command: "make lint"}}
	edited := append(slices.Clone(submitted), model.GateSpec{Name: "test", Command: "make test"})

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	restarted := &atomic.Bool{}
	h.facts = func(string) model.Project {
		if restarted.Load() {
			return model.Project{GateSteps: edited}
		}

		return model.Project{GateSteps: submitted}
	}

	gated := newHostGate()
	gated.plan = gatepkg.PlanOf(model.Project{GateSteps: submitted})
	deps := h.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	file := writeFlow(t, bodyFlow("c = gate(name = \"check\")\ncommand(run = [\"show\"] + c.required)\n"))
	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	receive(t, gated.started)

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	require.Error(t, receive(t, ended))

	restarted.Store(true)

	_, err = resumeAndJoin(t, engine, client)

	require.NoError(t, err)
	assert.Equal(t, []string{"show fmt lint"}, h.commands.ran(), "the required set is the one that the job was submitted with")
	assert.Equal(t, submitted, gated.waitedOn()[1].Plan.Steps, "the wait is given the recorded plan")
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
		_, runErr := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	receive(t, gated.started)

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	settleCancel(t, engine, gated, ended)

	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err)

	assert.Equal(t, []string{"gpu-b"}, own.asked, "the resume read the recorded host and started none")

	hosts := gated.gatedOn()
	require.GreaterOrEqual(t, len(hosts), 2, "the gate ran before and after the resume")

	for _, host := range hosts {
		assert.Equal(t, "gpu-b", host, "the resumed run gates on the host that the first run recorded")
	}
}

// settleCancel waits for the end of the main run that a cancel stopped, then for every run of its tree to leave the queue as canceled and for the wait
// that the cancel stopped to return, so that nothing of the old run is alive when the test resumes. It prints the state of the engine if the test fails.
func settleCancel(t *testing.T, engine *Engine, gated hostGate, ended <-chan error) {
	t.Helper()

	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("the engine at the end of the failed test:\n%s", within(dumpBound, func() string { return workflowState(engine) }))
		}
	})

	require.Error(t, receive(t, ended))

	waitOn(t, waiting[struct{}]{what: "the old runs of the item to end as canceled and the old wait to return", read: func() (struct{}, bool, error) {
		return struct{}{}, gated.oldWaitReturned() && noRunOpen(engine), nil
	}})
}

// oldWaitReturned reports whether the wait that the cancel stopped has returned. The first call tells the test that it waits for that return.
func (g hostGate) oldWaitReturned() bool {
	if g.returned.Load() > 0 {
		return true
	}

	select {
	case g.waiting <- struct{}{}:
	default:
	}

	return false
}

// noRunOpen reports whether every workflow of the engine has ended.
func noRunOpen(engine *Engine) bool {
	rows, err := dbos.ListWorkflows(engine.ctx)
	if err != nil {
		return false
	}

	return !slices.ContainsFunc(rows, func(row dbos.WorkflowStatus) bool {
		return row.Status != dbos.WorkflowStatusCancelled && row.Status != dbos.WorkflowStatusSuccess && row.Status != dbos.WorkflowStatusError
	})
}

// The old wait of the job stays open after the cancel, until the resumed run starts its own wait on the same job or the test begins to wait for the old wait to return.
// The old wait must return first: a resume that starts while it runs reads a job that a stopped run still holds.
func TestGateJob_AResumeStartsOnlyAfterTheWaitThatTheCancelStoppedHasReturned(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	own := &upHosts{started: true}
	h.hosts = own
	gated := newHostGate()
	gated.hold = make(chan struct{})
	deps := h.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	go func() {
		select {
		case <-gated.later:
		case <-gated.waiting:
		case <-t.Context().Done():
		}

		close(gated.hold)
	}()

	file := writeFlow(t, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\ngate(name = \"check\")\n"))
	ended := make(chan error, 1)

	go func() {
		_, runErr := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	receive(t, gated.started)

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	settleCancel(t, engine, gated, ended)

	_, err = resumeAndJoin(t, engine, client)
	require.NoError(t, err)

	events := gated.eventsSeen()
	require.Contains(t, events, "wait 1 returns")
	require.Contains(t, events, "wait 2 starts")
	assert.Less(t, slices.Index(events, "wait 1 returns"), slices.Index(events, "wait 2 starts"), "the old wait returned before the resumed run waited on the job: %v", events)
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
		_, runErr := engine.RunFile(testItem, testProject, file, modeOff())
		ended <- runErr
	}()

	receive(t, gated.started)

	client := h.client(t, testVersion)
	require.NoError(t, client.Cancel(testItem))
	h.tick(t)

	settleCancel(t, engine, gated, ended)

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

	_, err := engine.RunFile(testItem, testProject, writeFlow(t, bodyFlow("gate(name = \"check\")\n")), modeOff())
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
