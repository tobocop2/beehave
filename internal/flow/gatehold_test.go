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

// waitProbe runs the wait of a gate inside a run, because the wait reads the run id of its context.
type waitProbe struct{ engine *Engine }

// canceled waits for a build job whose result is a cancel, in a run that is no gate.
func (p waitProbe) canceled(ctx dbos.Context, _ string) (string, error) {
	_, err := p.engine.awaitHeld(ctx, canceledJob{}, "no-such-queue")

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

func TestGateHold_AHeldGateFreesThePlaceOfItsProjectAndResumesAfterReleaseWithItsRunIntact(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1", "2"))

	first := queueHostGate(rig.engine, "project-a", "1", "A", "box")
	rig.expect(t, "start 1")

	running := rig.attemptsOfRunningJob(t)

	require.NoError(t, rig.client.HoldGate("1"))
	rig.h.tick(t)
	receive(t, running)

	second := queueHostGate(rig.engine, "project-a", "2", "A", "box")
	rig.expect(t, "start 2")
	rig.let("2")
	rig.expect(t, "end 2")
	require.NoError(t, receive(t, second).err)

	select {
	case end := <-first:
		t.Fatalf("the run that awaits the held gate ended while the gate was held: %v", end.err)
	default:
	}

	require.NoError(t, rig.client.ReleaseGate("1"))
	rig.expect(t, "start 1")
	rig.let("1")
	rig.expect(t, "end 1")

	require.NoError(t, receive(t, first).err, "the gate resumes and finishes: its run is not superseded")
}

func TestGateHold_AHeldGateStaysHeldAcrossARestartOfTheEngine(t *testing.T) {
	t.Parallel()

	gated := newStopGate("1", "2")
	rig := newHoldRig(t, gated)

	ended := queueHostGate(rig.engine, "project-a", "1", "A", "box")
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

	other := queueHostGate(second, "project-a", "2", "A", "box")
	rig.expect(t, "start 2")
	rig.let("2")
	rig.expect(t, "end 2")
	require.NoError(t, receive(t, other).err, "the place of the project stays free while the gate is held, across the restart")

	require.NoError(t, rig.client.ReleaseGate("1"))
	rig.expect(t, "start 1")
	rig.let("1")
	rig.expect(t, "end 1")
}

func TestGateHold_ACancelOfAHeldGateEndsTheAwaitingRunAsSuperseded(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	ended := rig.queue("1")
	rig.expect(t, "start 1")

	gate, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)

	running := rig.attemptsOfRunningJob(t)

	require.NoError(t, rig.client.HoldGate("1"))
	rig.h.tick(t)
	receive(t, running)

	setHead(rig.h, "B")
	require.NoError(t, dbos.CancelWorkflows(rig.engine.ctx, []string{gate.row.ID}, dbos.WithCancelChildren()))

	require.ErrorIs(t, endsAfterTicks(t, rig.h, ended).err, errSuperseded, "a cancel of a held gate is a stop, not a hold")
}

func TestGateHold_AReleaseBeforeTheOldRunSeesTheCancelOpensTheJobOnce(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	ended := rig.queue("1")
	rig.expect(t, "start 1")

	require.NoError(t, rig.client.HoldGate("1"))
	require.NoError(t, rig.client.ReleaseGate("1"))
	rig.h.tick(t)

	rig.expect(t, "start 1")
	rig.let("1")
	rig.expect(t, "end 1")
	require.NoError(t, receive(t, ended).err)

	jobs, err := dbos.ListWorkflows(rig.engine.ctx, dbos.WithFilterName(hostJobName))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, dbos.WorkflowStatusSuccess, jobs[0].Status)
	assert.Empty(t, rig.gated.events, "the job ran the gate once more, not twice")
}

func TestGateHold_AReleaseBeforeAnyTickRunsTheBuildJobOnce(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	ended := rig.queue("1")
	rig.expect(t, "start 1")

	require.NoError(t, rig.client.HoldGate("1"))
	require.NoError(t, rig.client.ReleaseGate("1"))

	rig.expect(t, "start 1")
	rig.let("1")
	rig.expect(t, "end 1")
	require.NoError(t, receive(t, ended).err)

	require.Never(t, func() bool { return len(rig.gated.events) > 0 }, 3*storePoll, 100*time.Millisecond, "the earlier execution of the job ended before the job reopened, so no second execution ends")
}

func TestGateHold_ARunThatAReleaseReplacedEndsWhileTheJobRuns(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	ended := rig.queue("1")
	rig.expect(t, "start 1")

	gate, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)

	var old *attempt

	for _, a := range rig.engine.cancels.attempts() {
		if a.id == gate.row.ID {
			old = a
		}
	}

	require.NotNil(t, old, "the engine lists the run of the gate")

	require.NoError(t, rig.client.HoldGate("1"))
	require.NoError(t, rig.client.ReleaseGate("1"))
	rig.expect(t, "start 1")

	select {
	case <-old.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the run that the release replaced still waits for the job: it must end and leave the wait to the new run")
	}

	rig.let("1")
	rig.expect(t, "end 1")
	require.NoError(t, receive(t, ended).err)
}

func TestGateHold_AReleaseBehindAnotherGateOfTheProjectLeavesTheJobCanceledUntilTheGateStartsAgain(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1", "2"))

	first := queueHostGate(rig.engine, "project-a", "1", "A", "box")
	rig.expect(t, "start 1")

	require.NoError(t, rig.client.HoldGate("1"))

	second := queueHostGate(rig.engine, "project-a", "2", "A", "box")
	rig.expect(t, "start 2")

	gate, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, rig.client.ReleaseGate("1"))

	require.Never(t, func() bool {
		jobs, err := rig.engine.store().List(dbos.WithFilterWorkflowIDs(gate.job.ID))

		return err != nil || len(jobs) != 1 || jobs[0].Status != dbos.WorkflowStatusCancelled
	}, 3*storePoll, 100*time.Millisecond, "a gate that waits for its place on the queue of its project does not reopen its job")

	rig.let("2")
	rig.expect(t, "end 2")
	require.NoError(t, receive(t, second).err)

	rig.expect(t, "start 1")
	rig.let("1")
	rig.expect(t, "end 1")
	require.NoError(t, receive(t, first).err)
}

func TestGateHold_AMoveOfTheItemKeepsAHeldGateHeldAndItsJobCanceled(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1", "2"))

	rig.queue("1")
	rig.expect(t, "start 1")

	require.NoError(t, rig.client.HoldGate("1"))

	gate, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)

	parent, err := rig.client.row(gate.row.ParentWorkflowID)
	require.NoError(t, err)

	require.NoError(t, rig.client.markMove([]dbos.WorkflowStatus{parent, gate.row}))
	require.NoError(t, dbos.CancelWorkflow(rig.client.flow, parent.ID, dbos.WithCancelChildren(), dbos.WithCancelFailed()))
	require.NoError(t, rig.client.reopenCanceledPhases(parent.ID))

	moved, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok, "the gate is open again")
	assert.True(t, moved.held(), "the move does not release a held gate")

	jobs, err := rig.engine.store().List(dbos.WithFilterWorkflowIDs(gate.job.ID))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, dbos.WorkflowStatusCancelled, jobs[0].Status, "the move does not reopen the job of a held gate")

	require.Never(t, func() bool { return len(rig.gated.events) > 0 }, 3*storePoll, 100*time.Millisecond, "a held gate starts no job after a move")
}

func TestGateHold_AGateThatRunsWaitsForAJobThatIsOpen(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	rig.queue("1")
	rig.expect(t, "start 1")

	gate, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)

	reopened, err := rig.engine.reopenGate(gate.row.ID, gate.job.ID, slotQueueName(model.GateClass, 1), startOf(t, rig.engine, gate.row.ID))
	require.NoError(t, err)
	assert.True(t, reopened, "the run that has the gate waits for a job that is open")

	job, err := rig.engine.store().List(dbos.WithFilterWorkflowIDs(gate.job.ID))
	require.NoError(t, err)
	require.Len(t, job, 1)
	assert.Equal(t, dbos.WorkflowStatusPending, job[0].Status, "an open job is not reopened")
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

func TestGateHold_AReopenCutsTheEarlierExecutionOfTheJobAndWaitsForItsEnd(t *testing.T) {
	t.Parallel()

	newAttempt := func(id string) *attempt {
		return &attempt{id: id, cancel: func() {}, ended: make(chan struct{}), gate: true}
	}

	var list cancels

	live, other, done := newAttempt("job"), newAttempt("other"), newAttempt("job")
	close(done.ended)

	list.add(done)
	assert.Empty(t, list.stop("job"), "an execution that has ended is not waited for")

	list.add(other)
	list.add(live)
	assert.Equal(t, []*attempt{other}, list.stop("other"), "the execution of the run is cut and waited for")
	assert.Equal(t, []*attempt{live}, list.stop("job"), "the execution of another run is not waited for")
	assert.Equal(t, []*attempt{live}, list.stop("job"), "a cut execution that has not ended stays in the list")

	close(live.ended)
	list.add(newAttempt("later"))
	assert.Empty(t, list.stop("job"), "the add of a run drops the executions that ended")

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, waitEnded(t.Context(), []*attempt{done}))
	require.ErrorIs(t, waitEnded(canceled, []*attempt{newAttempt("job")}), context.Canceled)

	engine := idleEngine(t, newHarness(t))
	engine.cancels.add(newAttempt("job"))
	engine.deps.Clock.JobStop = 50 * time.Millisecond

	err := engine.stopJob("job")
	require.ErrorContains(t, err, "did not stop within 50ms", "a program that ignores its cancel does not hold the wait for longer than the limit")
	require.NotErrorIs(t, err, context.DeadlineExceeded)

	engine.cancels.add(newAttempt("job"))
	engine.deps.Clock.JobStop = time.Hour
	require.NoError(t, engine.Shutdown())

	require.ErrorIs(t, engine.stopJob("job"), context.Canceled, "a reopen that the engine's stop interrupts waits no longer")
}

// slowStopGate is a stop gate whose program ends after a cancel only when the test closes proceed. It sends "canceled <item>" when the cancel arrives
// and "stopped <item>" when the program ends.
type slowStopGate struct {
	stopGate
	proceed chan struct{}
}

func (g slowStopGate) Run(ctx context.Context, _, item, _ string, _ GateJob) (GateResult, error) {
	g.events <- "start " + item

	select {
	case <-g.release[item]:
	case <-ctx.Done():
		g.events <- "canceled " + item

		<-g.proceed

		g.events <- "stopped " + item

		return GateResult{}, ctx.Err()
	}

	g.events <- "end " + item

	return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
}

// newSlowHoldRig is a hold rig whose gate program stops when the test lets it.
func newSlowHoldRig(t *testing.T, gated stopGate, proceed chan struct{}, jobStop time.Duration) holdRig {
	t.Helper()

	h := newHarness(t)
	h.jobStop = jobStop
	h.forge.state = review.Standing{Number: testPull, Head: "A"}
	h.slots = gateCounts(map[string]*atomic.Int32{"box": slotCount(1)})

	deps := h.deps()
	deps.Gate = slowStopGate{stopGate: gated, proceed: proceed}

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, gateQueuer{engine}.queue, dbos.WithWorkflowName("test-queue-gate"))
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	client, err := OpenClient(t.Context(), h.opts, ReleaseDeps{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	return holdRig{h: h, engine: engine, client: client, gated: gated}
}

// startOf is the start that the store holds for the run id.
func startOf(t *testing.T, e *Engine, id string) time.Time {
	t.Helper()

	rows, err := e.store().List(dbos.WithFilterWorkflowIDs(id))
	require.NoError(t, err)

	row, found := rowWithID(rows, id)
	require.True(t, found)

	return row.StartedAt
}

func TestGateHold_AProgramThatDoesNotStopFailsTheGateWithinTheLimitAndLeavesTheJobCanceled(t *testing.T) {
	t.Parallel()

	proceed := make(chan struct{})
	rig := newSlowHoldRig(t, newStopGate("1"), proceed, 300*time.Millisecond)

	t.Cleanup(func() { close(proceed) })

	ended := rig.queue("1")
	rig.expect(t, "start 1")

	gate, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, rig.client.HoldGate("1"))
	require.NoError(t, rig.client.ReleaseGate("1"))
	rig.expect(t, "canceled 1")

	end := receive(t, ended)
	require.ErrorContains(t, end.err, "did not stop within 300ms", "the wait for a program that ignores its cancel ends at the limit")

	jobs, err := rig.engine.store().List(dbos.WithFilterWorkflowIDs(gate.job.ID))
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, dbos.WorkflowStatusCancelled, jobs[0].Status, "a job whose earlier run did not stop is not reopened")
}

func TestGateHold_ACancelOfTheGateWhileItWaitsForTheStopOfTheJobOpensNoJob(t *testing.T) {
	t.Parallel()

	proceed := make(chan struct{})
	rig := newSlowHoldRig(t, newStopGate("1"), proceed, 0)

	rig.queue("1")
	rig.expect(t, "start 1")

	gate, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, rig.client.HoldGate("1"))
	require.NoError(t, rig.client.ReleaseGate("1"))
	rig.expect(t, "canceled 1")

	require.NoError(t, dbos.CancelWorkflows(rig.engine.ctx, []string{gate.row.ID}, dbos.WithCancelChildren()))

	close(proceed)
	rig.expect(t, "stopped 1")

	require.Never(t, func() bool {
		rows, err := rig.engine.store().List(dbos.WithFilterWorkflowIDs(gate.row.ID, gate.job.ID))
		if err != nil || len(rows) != 2 {
			return true
		}

		job, _ := rowWithID(rows, gate.job.ID)

		return job.Status != dbos.WorkflowStatusCancelled || len(rig.gated.events) > 0
	}, 3*storePoll, 100*time.Millisecond, "a canceled gate leaves its job canceled and starts no job")
}

func TestGateHold_ARunThatAReleaseReplacedNeverResumesTheJobAfterTheNewRunStartedIt(t *testing.T) {
	t.Parallel()

	proceed := make(chan struct{})
	rig := newSlowHoldRig(t, newStopGate("1"), proceed, 0)

	ended := rig.queue("1")
	rig.expect(t, "start 1")

	gate, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)

	replaced := startOf(t, rig.engine, gate.row.ID)

	require.NoError(t, rig.client.HoldGate("1"))
	require.NoError(t, rig.client.ReleaseGate("1"))
	rig.expect(t, "canceled 1")

	// The replaced run waits on a stop that the test holds, as the new run waits on the stop of the program.
	late := &attempt{id: gate.job.ID, cancel: func() {}, ended: make(chan struct{}), gate: true}
	rig.engine.cancels.add(late)

	type reopen struct {
		reopened bool
		err      error
	}

	result := make(chan reopen, 1)

	go func() {
		reopened, err := rig.engine.reopenGate(gate.row.ID, gate.job.ID, slotQueueName(model.GateClass, 1), replaced)
		result <- reopen{reopened, err}
	}()

	close(proceed)
	rig.expect(t, "stopped 1")
	rig.expect(t, "start 1")

	close(late.ended)

	got := receive(t, result)
	assert.False(t, got.reopened)
	require.ErrorIs(t, got.err, errReplaced, "the run that the release replaced does not resume a job that the new run started")

	require.Never(t, func() bool { return len(rig.gated.events) > 0 }, 3*storePoll, 100*time.Millisecond, "the job starts once: no second resume re-queues it")

	rig.let("1")
	rig.expect(t, "end 1")
	require.NoError(t, receive(t, ended).err)
}

func TestGateHold_AResumeThatLandsAfterACancelOrAHoldOfTheGateCancelsTheJobItOpened(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	rig.queue("1")
	rig.expect(t, "start 1")

	gate, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)

	began := startOf(t, rig.engine, gate.row.ID)

	require.NoError(t, rig.client.HoldGate("1"))

	reopened, err := rig.engine.resumeJob(gate.row.ID, gate.job.ID, slotQueueName(model.GateClass, 1), began)
	assert.False(t, reopened)
	require.ErrorIs(t, err, errReplaced, "a gate that was parked after the read does not keep the job that the resume opened")

	rows, err := rig.engine.store().List(dbos.WithFilterWorkflowIDs(gate.job.ID))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, dbos.WorkflowStatusCancelled, rows[0].Status)

	reopened, err = rig.engine.resumeJob(gate.row.ID, "no-such-job", slotQueueName(model.GateClass, 1), began)
	assert.False(t, reopened)
	require.ErrorContains(t, err, "reopen the build job of the gate")
}

func TestGateHold_AWaitThatCannotReadTheGateFailsInsteadOfWaiting(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	_, err := engine.awaitHeld(engine.ctx, canceledJob{}, "queue")
	require.ErrorContains(t, err, "read the run id of the gate")

	require.NoError(t, engine.Shutdown())

	_, err = engine.runStartedAt("gate")
	require.ErrorContains(t, err, "read the start of the gate")

	reopened, err := engine.reopenGate("gate", "job", "queue", time.Time{})

	assert.False(t, reopened)
	require.ErrorContains(t, err, "read the build job of the gate")
}

func TestGateHold_AHoldRefusesAGateWhoseJobWasCanceledAndReportsAGateThatTheStoreDoesNotHold(t *testing.T) {
	t.Parallel()

	canceled := openGate{job: &dbos.WorkflowStatus{Status: dbos.WorkflowStatusCancelled}}
	err := canceled.refuseHold("1")
	require.ErrorIs(t, err, errGateState)
	require.ErrorContains(t, err, "has no job that runs")

	started := openGate{row: dbos.WorkflowStatus{Status: dbos.WorkflowStatusPending}, job: canceled.job}
	require.ErrorContains(t, started.refuseHold("1"), "started again and has not reopened its job yet")

	rig := newHoldRig(t, newStopGate("1"))

	err = rig.client.parkAndStop(openGate{row: dbos.WorkflowStatus{ID: "no-such-gate"}}, "1")
	require.ErrorContains(t, err, "park the gate of item 1")
}

func TestGateHold_TheCancelOfAJobStandsForARunThatIsNoGateAndForAGateThatTheStoreDoesNotHold(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	await, err := dbos.RunWorkflow(rig.engine.ctx, waitProbe{rig.engine}.canceled, "")
	require.NoError(t, err)

	_, err = await.GetResult()
	require.ErrorContains(t, err, dbos.ErrAwaitedWorkflowCancelled.Error(), "a run that is no gate does not reopen a canceled job")

	reopened, err := rig.engine.reopenGate("no-such-gate", "no-such-job", "queue", time.Time{})
	require.NoError(t, err)
	assert.False(t, reopened, "a gate that the store does not hold has no job to reopen")
}

func TestGateHold_AParkedGateEndsTheRunThatSeesItsJobCanceledAndAReleasedGateReopensTheJob(t *testing.T) {
	t.Parallel()

	rig := newHoldRig(t, newStopGate("1"))

	rig.queue("1")
	rig.expect(t, "start 1")

	gate, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, rig.client.HoldGate("1"))

	reopened, err := rig.engine.reopenGate(gate.row.ID, gate.job.ID, slotQueueName(model.GateClass, 1), startOf(t, rig.engine, gate.row.ID))
	assert.False(t, reopened)
	require.ErrorIs(t, err, errReplaced, "the run of a parked gate ends and leaves the gate to its release")

	parked, ok, err := rig.client.readGate("1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.True(t, parked.held())
	assert.Equal(t, dbos.WorkflowStatusEnqueued, parked.row.Status)
}

func TestGateHold_ACallThatIsNoRunHasNoGateToRead(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))

	assert.Equal(t, uint(6), engine.currentPlace(engine.ctx, 6), "a context with no run id keeps the place that the gate was queued with")

	assert.Equal(t, uint(6), engine.storedPlace("no-such-run", 6), "a run that the store does not hold keeps the place that the gate was queued with")

	require.NoError(t, engine.Shutdown())

	assert.Equal(t, uint(6), engine.storedPlace("any-run", 6), "a store that cannot be read keeps the place that the gate was queued with")
}
