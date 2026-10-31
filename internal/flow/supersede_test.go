package flow

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/review"
)

// supersedeGate holds each gate until the test lets it end or its context ends, and counts the gates that started and the ones that its context stopped.
type supersedeGate struct {
	started *atomic.Int32
	stopped *atomic.Int32
	release chan struct{}
	tip     string
}

func (g supersedeGate) Key(context.Context, string, string) (GateKey, error) {
	return GateKey{Head: g.tip}, nil
}

func (g supersedeGate) Run(ctx context.Context, _, _ string) (GateResult, error) {
	g.started.Add(1)

	select {
	case <-g.release:
		return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
	case <-ctx.Done():
		g.stopped.Add(1)

		return GateResult{}, ctx.Err()
	}
}

func newSupersedeGate(tip string) supersedeGate {
	return supersedeGate{started: &atomic.Int32{}, stopped: &atomic.Int32{}, release: make(chan struct{}), tip: tip}
}

type gateEnd struct {
	out ExclusiveOutput
	err error
}

// gateEnds holds the channel of each test run that queues a gate, by the id of the run.
var (
	gateEnds   = map[string]chan gateEnd{}
	gateEndsMu sync.Mutex
	gateRuns   atomic.Int64
)

// gateQueuer is the run that queues one gate, as a run of a flow does: the head read and the cancel are recorded steps of the run that queues.
type gateQueuer struct{ engine *Engine }

func (q gateQueuer) queue(ctx dbos.Context, in ExclusiveInput) (string, error) {
	id, err := dbos.GetWorkflowID(ctx)
	if err != nil {
		return "", err //nolint:wrapcheck // a test helper
	}

	gateEndsMu.Lock()
	end := gateEnds[id]
	gateEndsMu.Unlock()

	out, err := q.engine.queueExclusive(ctx, in)
	end <- gateEnd{out: out, err: err}

	return "", nil
}

// queueGate queues the gate of head of the test item on the exclusive queue and returns the channel of its end.
func queueGate(engine *Engine, head string) <-chan gateEnd {
	return queueGateOf(engine, testProject, testItem, head)
}

// queueGateOf queues the gate of an item of a project from a run, and returns the channel of its end.
func queueGateOf(engine *Engine, project, item, head string) <-chan gateEnd {
	ended := make(chan gateEnd, 1)
	id := fmt.Sprintf("queue-gate-%d", gateRuns.Add(1))
	gateEndsMu.Lock()
	gateEnds[id] = ended
	gateEndsMu.Unlock()

	_, err := dbos.RunWorkflow(engine.ctx, gateQueuer{engine}.queue, ExclusiveInput{Kind: kindGate, Project: project, Item: item, Head: head, Request: CommandRequest{Project: project, Item: item}}, dbos.WithWorkflowID(id))
	if err != nil {
		ended <- gateEnd{err: err}
	}

	return ended
}

func supersedeEngine(t *testing.T, gated supersedeGate, head string) (*harness, *Engine) {
	t.Helper()

	return supersedeEngineWith(t, gated, head, nil)
}

// supersedeEngineWith builds the engine of a supersede test. A non-nil wrap puts a forge around the fake one.
func supersedeEngineWith(t *testing.T, gated supersedeGate, head string, wrap func(*fakeForge) forge) (*harness, *Engine) {
	t.Helper()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: head}
	deps := h.deps()
	deps.Gate = gated

	if wrap != nil {
		deps.Forge = wrap(h.forge)
	}

	h.migrated(t)

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, gateQueuer{engine}.queue, dbos.WithWorkflowName("test-queue-gate"))
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return h, engine
}

func startedCount(g supersedeGate, want int32) func() bool {
	return func() bool { return g.started.Load() == want }
}

// waitsInTheQueue is true when the queue holds want gates that wait for the gate that runs. A gate that is not yet queued cannot be stopped by a later gate.
func waitsInTheQueue(engine *Engine, want int) func() bool {
	return func() bool {
		rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName), dbos.WithFilterStatus(dbos.WorkflowStatusEnqueued))

		return err == nil && len(rows) == want
	}
}

// endsAfterTicks ticks the engine until the gate ends, and returns how it ended.
func endsAfterTicks(t *testing.T, h *harness, ended <-chan gateEnd) gateEnd {
	t.Helper()

	deadline := time.After(20 * time.Second)

	for {
		select {
		case end := <-ended:
			return end
		case <-deadline:
			t.Fatal("the gate did not end")
		default:
			h.tick(t)
		}
	}
}

func TestSuperseded_AGateThatRunsForAnOldHeadStopsWhenTheGateOfTheNewHeadIsQueued(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	old := queueGate(engine, "A")

	eventually(t, startedCount(gated, 1))

	h.forge.mu.Lock()
	h.forge.state.Head = "B"
	h.forge.mu.Unlock()

	fresh := queueGate(engine, "B")

	end := endsAfterTicks(t, h, old)
	require.ErrorIs(t, end.err, errSuperseded)
	require.EqualError(t, end.err, "superseded by B")
	// The run that waited ends when DBOS marks the gate canceled. The gate counts its stop when the tick cancels its context, a moment later.
	eventually(t, func() bool { return gated.stopped.Load() == 1 })

	eventually(t, startedCount(gated, 2))
	close(gated.release)

	newer := <-fresh
	require.NoError(t, newer.err)
	assert.Equal(t, true, newer.out.Outputs["passed"])
}

func TestSuperseded_AGateThatWaitsInTheQueueForAnOldHeadNeverStarts(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	first := queueGate(engine, "A")

	eventually(t, startedCount(gated, 1))

	// The pull request already has head C, so the gate of head B that queues now is late: it stops no gate and waits behind the gate of head A.
	h.forge.mu.Lock()
	h.forge.state.Head = "C"
	h.forge.mu.Unlock()

	waiting := queueGate(engine, "B")

	eventually(t, waitsInTheQueue(engine, 1))

	last := queueGate(engine, "C")

	skipped := endsAfterTicks(t, h, waiting)
	require.ErrorIs(t, skipped.err, errSuperseded)
	require.EqualError(t, skipped.err, "superseded by C")

	require.ErrorIs(t, endsAfterTicks(t, h, first).err, errSuperseded)
	eventually(t, startedCount(gated, 2))
	close(gated.release)
	require.NoError(t, (<-last).err)
	assert.EqualValues(t, 2, gated.started.Load(), "the gate of head B never started")
}

func TestSuperseded_AGateOfTheSameHeadIsNotStopped(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "A")

	first := queueGate(engine, "A")

	eventually(t, startedCount(gated, 1))

	second := queueGate(engine, "A")

	time.Sleep(3 * time.Second)
	close(gated.release)

	require.NoError(t, (<-first).err)
	require.NoError(t, (<-second).err)
	assert.Zero(t, gated.stopped.Load())
}

func TestSuperseded_AGateOfAnOldHeadThatQueuesLateStopsNoGateOfTheCurrentHead(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	_, engine := supersedeEngine(t, gated, "B")

	current := queueGate(engine, "B")

	eventually(t, startedCount(gated, 1))

	late := queueGate(engine, "A")

	time.Sleep(3 * time.Second)
	close(gated.release)

	require.NoError(t, (<-current).err)
	require.NoError(t, (<-late).err)
	assert.Zero(t, gated.stopped.Load())
}

func TestSuperseded_AGateThatNamesNoHeadTakesTheHeadOfThePullRequest(t *testing.T) {
	t.Parallel()

	standing := review.Standing{Head: "B"}

	assert.Equal(t, "B", tipOf(ExclusiveInput{Kind: kindGate}, standing, true))
	assert.Equal(t, "A", tipOf(ExclusiveInput{Kind: kindGate, Head: "A"}, standing, true))
	assert.Empty(t, tipOf(ExclusiveInput{Kind: kindGate}, standing, false), "a pull request that cannot be read gives no tip")
	assert.Empty(t, tipOf(ExclusiveInput{Kind: kindMerge, Head: "A"}, standing, true))
}

func TestSuperseded_AGateThatAPersonCancelsKeepsTheErrorOfTheCancel(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	only := queueGate(engine, "A")

	eventually(t, startedCount(gated, 1))

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName), dbos.WithFilterStatus(dbos.WorkflowStatusPending))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NoError(t, dbos.CancelWorkflow(engine.ctx, rows[0].ID))

	end := endsAfterTicks(t, h, only)
	require.ErrorIs(t, end.err, dbos.ErrAwaitedWorkflowCancelled)
	require.NotErrorIs(t, end.err, errSuperseded)
}

func TestSuperseded_AStoreThatCannotBeListedLeavesTheOldGatesAloneAndSaysSo(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "B"}

	engine, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, engine.Shutdown())

	var logged bytes.Buffer

	engine.logger = newLogger(&logged)

	stopped := engine.stopSuperseded(t.Context(), ExclusiveInput{Kind: kindGate, Project: testProject, Item: testItem, Tip: "B"})

	assert.Empty(t, stopped)
	assert.Contains(t, logged.String(), "cannot stop the old gates of the pull request")
}

func TestSuperseded_AQueuedRunThatIsNotAGateIsNotRead(t *testing.T) {
	t.Parallel()

	_, ok := gateInputOf(`{"kind":"merge","project":"p","item":"12","tip":"A"}`)
	assert.False(t, ok)

	_, ok = gateInputOf("not json")
	assert.False(t, ok)

	_, ok = gateInputOf(42)
	assert.False(t, ok)
}

func TestSuperseded_ACancelledGateIsNeverReusedAsAPass(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	old := queueGate(engine, "A")

	eventually(t, startedCount(gated, 1))

	h.forge.mu.Lock()
	h.forge.state.Head = "B"
	h.forge.mu.Unlock()

	_ = queueGate(engine, "B")

	require.ErrorIs(t, endsAfterTicks(t, h, old).err, errSuperseded)

	key := GateKey{ID: "key-1", Head: "A", Tree: "tree-1"}
	_, found := engine.earlierPass(key, testProject)
	assert.False(t, found, "only a run that finished with a pass is read for reuse")
}
