package flow

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

// stopGate holds the gate of each item until the test lets it end. It sends "start <item>" as a gate starts and "end <item>" as it ends.
// A gate that its context stops sends no "end".
type stopGate struct {
	events  chan string
	release map[string]chan struct{}
}

func newStopGate(items ...string) stopGate {
	release := map[string]chan struct{}{}
	for _, item := range items {
		release[item] = make(chan struct{})
	}

	return stopGate{events: make(chan string, 32), release: release}
}

func (stopGate) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (stopGate) Submit(_ context.Context, _, item, _, _, _, _ string, _ model.Slot) (GateJob, error) {
	return GateJob{ID: "job-" + item, Head: gatedHead}, nil
}

func (g stopGate) Run(ctx context.Context, _, item, _ string, _ GateJob) (GateResult, error) {
	g.events <- "start " + item

	select {
	case <-g.release[item]:
	case <-ctx.Done():
		return GateResult{}, ctx.Err()
	}

	g.events <- "end " + item

	return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
}

// holdRig is an engine with one slot on the host box and a client of its state folder.
type holdRig struct {
	h      *harness
	engine *Engine
	client *Client
	gated  stopGate
}

func newHoldRig(t *testing.T, gated stopGate) holdRig {
	t.Helper()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	h.slots = gateCounts(map[string]*atomic.Int32{"box": slotCount(1)})

	deps := h.deps()
	deps.Gate = gated

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, gateQueuer{engine}.queue, dbos.WithWorkflowName("test-queue-gate"))
	dbos.RegisterWorkflow(engine.ctx, waitProbe{engine}.stopped, dbos.WithWorkflowName("test-wait-stopped"))
	dbos.RegisterWorkflow(engine.ctx, waitProbe{engine}.canceled, dbos.WithWorkflowName("test-await-canceled"))
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	client, err := OpenClient(t.Context(), h.opts, ReleaseDeps{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	return holdRig{h: h, engine: engine, client: client, gated: gated}
}

// queue queues the gate of an item of its own project on the host box.
func (r holdRig) queue(item string) <-chan gateEnd {
	return queueHostGate(r.engine, "project-"+item, item, "A", "box")
}

func (r holdRig) expect(t *testing.T, event string) {
	t.Helper()

	assert.Equal(t, event, receive(t, r.gated.events))
}

// attemptsOfRunningJob returns the channel that closes when the one build job that runs has returned. A release that comes before it can reopen a job whose first
// run is still ending, so a test of a release waits for it.
func (r holdRig) attemptsOfRunningJob(t *testing.T) <-chan struct{} {
	t.Helper()

	jobs, err := dbos.ListWorkflows(r.engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterStatus(dbos.WorkflowStatusPending))
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	for _, a := range r.engine.cancels.attempts() {
		if a.id == jobs[0].ID {
			return a.ended
		}
	}

	t.Fatalf("the engine lists no attempt of the running job %s", jobs[0].ID)

	return nil
}

// let ends the gate of an item.
func (r holdRig) let(item string) {
	close(r.gated.release[item])
}

// waitProbe runs the wait of a held gate inside a run, because the wait reads the run id of its context.
type waitProbe struct{ engine *Engine }

// stopped waits while job is held, with a context that is canceled already.
func (p waitProbe) stopped(ctx dbos.Context, job string) (string, error) {
	stopped, cancel := dbos.WithCancel(ctx)
	cancel()

	_, err := p.engine.waitWhileHeld(stopped, job)

	return "", err
}

// canceled waits for a build job whose result is a cancel, and which is not held.
func (p waitProbe) canceled(ctx dbos.Context, _ string) (string, error) {
	_, err := p.engine.awaitHeld(ctx, canceledJob{})

	return "", err
}

// canceledJob is a handle of a build job that was canceled with its gate.
type canceledJob struct {
	dbos.WorkflowHandle[ExclusiveOutput]
}

func (canceledJob) GetResult(...dbos.GetResultOption) (ExclusiveOutput, error) {
	return ExclusiveOutput{}, dbos.ErrAwaitedWorkflowCancelled
}

func (canceledJob) GetWorkflowID() string { return "no-such-job" }

func exclusiveQueued(engine *Engine, want int) func() bool {
	return func() bool {
		rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName), dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued))

		return err == nil && len(rows) == want
	}
}

func TestGateHold_ARaisedLastGateStartsFirstOnTheHostQueue(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1", "2", "3", "4"))

	ends := []<-chan gateEnd{rig.queue("1")}
	rig.expect(t, "start 1")

	ends = append(ends, rig.queue("2"), rig.queue("3"), rig.queue("4"))
	waitOn(t, condition(waitsOnHost(rig.engine, 3)))

	require.NoError(t, rig.client.SetGatePriority("4", "P0"))

	rig.let("1")
	rig.expect(t, "end 1")
	rig.expect(t, "start 4")

	rig.let("4")
	rig.let("2")
	rig.let("3")

	for _, end := range ends {
		require.NoError(t, receive(t, end).err)
	}
}

func TestGateHold_ARaisedGateOfOneProjectStartsBeforeAnEarlierOneAndItsJobTakesTheRaise(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1", "2", "3"))

	first := queueHostGate(rig.engine, testProject, "1", "A", "box")
	rig.expect(t, "start 1")

	second := queueHostGate(rig.engine, testProject, "2", "A", "box")
	third := queueHostGate(rig.engine, testProject, "3", "A", "box")
	waitOn(t, condition(exclusiveQueued(rig.engine, 2)))

	require.NoError(t, rig.client.SetGatePriority("3", "P0"))

	rig.let("1")
	rig.expect(t, "end 1")
	rig.expect(t, "start 3")

	jobs, err := dbos.ListWorkflows(rig.engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterStatus(dbos.WorkflowStatusPending))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, int(priorityP0), jobs[0].Priority, "the build job of the raised gate takes the place that the person set while the gate waited")

	rig.let("3")
	rig.expect(t, "end 3")
	rig.expect(t, "start 2")
	rig.let("2")

	for _, end := range []<-chan gateEnd{first, second, third} {
		require.NoError(t, receive(t, end).err)
	}
}

func TestGateHold_AHeldRunningGateFreesTheHostSlotAndReleaseKeepsItsPlace(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1", "2", "3"))

	first := rig.queue("1")
	rig.expect(t, "start 1")

	second := rig.queue("2")
	waitOn(t, condition(waitsOnHost(rig.engine, 1)))

	running := rig.attemptsOfRunningJob(t)

	require.NoError(t, rig.client.HoldGate("1"))
	rig.h.tick(t)
	receive(t, running)
	rig.expect(t, "start 2")

	third := rig.queue("3")
	waitOn(t, condition(waitsOnHost(rig.engine, 1)))

	require.NoError(t, rig.client.ReleaseGate("1"))
	waitOn(t, condition(waitsOnHost(rig.engine, 2)))

	rig.let("2")
	rig.expect(t, "end 2")
	rig.expect(t, "start 1")

	rig.let("1")
	rig.expect(t, "end 1")
	rig.expect(t, "start 3")
	rig.let("3")

	for _, end := range []<-chan gateEnd{first, second, third} {
		require.NoError(t, receive(t, end).err)
	}
}

func TestGateHold_AHeldGateStaysHeldAcrossARestartOfTheEngine(t *testing.T) {
	t.Parallel()

	gated := newStopGate("1")
	rig := newHoldRig(t, gated)

	ended := rig.queue("1")
	rig.expect(t, "start 1")

	require.NoError(t, rig.client.HoldGate("1"))
	rig.h.tick(t)

	require.NoError(t, rig.engine.Shutdown())
	require.Error(t, receive(t, ended).err)

	deps := rig.h.deps()
	deps.Gate = gated

	second, err := NewEngine(t.Context(), rig.h.opts, deps)
	require.NoError(t, err)
	dbos.RegisterWorkflow(second.ctx, gateQueuer{second}.queue, dbos.WithWorkflowName("test-queue-gate"))
	require.NoError(t, second.Launch())
	t.Cleanup(func() { require.NoError(t, second.Shutdown()) })

	require.Never(t, func() bool { return len(gated.events) > 0 }, 3*storePoll, 100*time.Millisecond, "the held gate does not start on the engine that came back")
	require.ErrorContains(t, rig.client.HoldGate("1"), "held already")

	require.NoError(t, rig.client.ReleaseGate("1"))
	rig.expect(t, "start 1")
	rig.let("1")
	rig.expect(t, "end 1")
}

func TestGateHold_EachCommandRefusesAGateInTheWrongState(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1", "2", "9"))

	rig.queue("1")
	rig.expect(t, "start 1")
	rig.queue("2")
	waitOn(t, condition(waitsOnHost(rig.engine, 1)))
	queueHostGate(rig.engine, "project-9", "9", "A", config.LocalHost)
	waitOn(t, condition(func() bool {
		_, open, err := rig.client.readGate("9")

		return err == nil && open
	}))

	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{"priority_of_an_item_with_no_gate", func() error { return rig.client.SetGatePriority("77", "P0") }, "no gate is open for item 77"},
		{"hold_of_an_item_with_no_gate", func() error { return rig.client.HoldGate("77") }, "no gate is open for item 77"},
		{"release_of_an_item_with_no_gate", func() error { return rig.client.ReleaseGate("77") }, "no gate is open for item 77"},
		{"priority_with_a_level_that_is_not_one", func() error { return rig.client.SetGatePriority("2", "urgent") }, `"urgent" is not a level`},
		{"priority_with_the_unknown_level", func() error { return rig.client.SetGatePriority("2", "P?") }, `"P?" is not a level`},
		{"priority_of_a_gate_that_runs", func() error { return rig.client.SetGatePriority("1", "P0") }, "a gate that runs keeps its place"},
		{"hold_of_a_gate_that_waits_for_its_host", func() error { return rig.client.HoldGate("2") }, "waits for its host and does not run"},
		{"hold_of_a_gate_that_runs_on_this_machine", func() error { return rig.client.HoldGate("9") }, "no build job on a host"},
		{"release_of_a_gate_that_is_not_held", func() error { return rig.client.ReleaseGate("1") }, "is not held"},
		{"release_of_a_gate_with_no_build_job", func() error { return rig.client.ReleaseGate("9") }, "is not held"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorContains(t, test.run(), test.want)
		})
	}
}

func TestGateHold_APriorityWriteThatTheStoreRefusesIsReported(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1", "2"))

	rig.queue("1")
	rig.expect(t, "start 1")
	rig.queue("2")
	waitOn(t, condition(waitsOnHost(rig.engine, 1)))

	rig.client.dir = t.TempDir()

	assert.ErrorContains(t, rig.client.SetGatePriority("2", "P0"), "set the place of the gate of item 2")
}

func TestGateHold_ACanceledGateEndsAndIsNotTakenForAHold(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	ended := rig.queue("1")
	rig.expect(t, "start 1")

	gates, err := dbos.ListWorkflows(rig.engine.ctx, dbos.WithFilterName(exclusiveName), dbos.WithFilterStatus(dbos.WorkflowStatusPending))
	require.NoError(t, err)
	require.Len(t, gates, 1)
	require.NoError(t, dbos.CancelWorkflows(rig.engine.ctx, []string{gates[0].ID}, dbos.WithCancelChildren()))

	end := endsAfterTicks(t, rig.h, ended)
	require.Error(t, end.err)
	assert.ErrorIs(t, end.err, dbos.ErrAwaitedWorkflowCancelled)
}

func TestGateHold_AClientWhoseStoreIsClosedRefusesEachCommand(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	client, err := OpenClient(t.Context(), rig.h.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	require.ErrorContains(t, client.SetGatePriority("1", "P0"), "read the gate of item 1")
	require.ErrorContains(t, client.HoldGate("1"), "read the gate of item 1")
	require.ErrorContains(t, client.ReleaseGate("1"), "read the gate of item 1")
}

func TestGateHold_AFailedStepNamesItself(t *testing.T) {
	t.Parallel()

	require.NoError(t, failed("hold the gate", nil))
	assert.EqualError(t, failed("hold the gate", errFake), "hold the gate: "+errFake.Error())
}

func TestGateHold_AWaitThatCannotReadTheGateFailsInsteadOfWaiting(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	released, err := engine.waitWhileHeld(engine.ctx, "job")

	assert.False(t, released)
	require.ErrorContains(t, err, "read the run id of the gate")
}

func TestGateHold_TheWaitOfAHeldGateEndsWhenItsContextEndsAndTheCancelOfAJobThatIsNotHeldStands(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	rig.queue("1")
	rig.expect(t, "start 1")
	require.NoError(t, rig.client.HoldGate("1"))

	jobs, err := dbos.ListWorkflows(rig.engine.ctx, dbos.WithFilterName(hostJobName), dbos.WithFilterStatus(dbos.WorkflowStatusCancelled))
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	wait, err := dbos.RunWorkflow(rig.engine.ctx, waitProbe{rig.engine}.stopped, jobs[0].ID)
	require.NoError(t, err)

	_, err = wait.GetResult()
	require.ErrorIs(t, err, context.Canceled, "a held gate stops waiting when its run stops")

	await, err := dbos.RunWorkflow(rig.engine.ctx, waitProbe{rig.engine}.canceled, "")
	require.NoError(t, err)

	_, err = await.GetResult()
	require.ErrorContains(t, err, dbos.ErrAwaitedWorkflowCancelled.Error())
}

func TestGateHold_ACallThatIsNoRunHasNoGateToRead(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	assert.Equal(t, uint(6), engine.currentPlace(engine.ctx, 6), "a context with no run id keeps the place that the gate was queued with")

	_, _, err := engine.holdOf(engine.ctx, "job")
	require.ErrorContains(t, err, "read the run id of the gate")

	assert.Equal(t, uint(6), engine.storedPlace("no-such-run", 6), "a run that the store does not hold keeps the place that the gate was queued with")

	require.NoError(t, engine.Shutdown())

	assert.Equal(t, uint(6), engine.storedPlace("any-run", 6), "a store that cannot be read keeps the place that the gate was queued with")

	_, _, err = engine.holdAt("gate", "job")
	assert.ErrorContains(t, err, "read the build job of the gate")
}
