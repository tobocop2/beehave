package flow

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/review"
)

func setHead(h *harness, head string) {
	h.forge.mu.Lock()
	defer h.forge.mu.Unlock()

	h.forge.state.Head = head
}

func TestSuperseded_AGateOfAnotherItemOfTheProjectIsNotStopped(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	first := queueGate(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))

	// The other item has its own pull request and its own tip. It waits behind the first gate in the queue of the project.
	other := queueGateOf(engine, testProject, "99", "X")

	waitOn(t, condition(waitsInTheQueue(engine, 1)))
	setHead(h, "B")

	current := queueGate(engine, "B")

	require.ErrorIs(t, endsAfterTicks(t, h, first).err, errSuperseded)

	// The tick after the end has canceled the context of the gate. The gate counts its stop a moment later.
	waitOn(t, stopsReach(t, engine, gated, 1))

	close(gated.release)

	require.NoError(t, receiveEnd(t, engine, gated, other).err, "a gate of another item is not a gate of the same pull request")
	require.NoError(t, receiveEnd(t, engine, gated, current).err)
	assert.EqualValues(t, 1, gated.stopped.Load(), "only the gate of the same item stopped")
}

func TestSuperseded_AGateOfAnotherProjectIsNotStopped(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	first := queueGate(engine, "A")
	foreign := queueGateOf(engine, "other-project", testItem, "X")

	waitOn(t, startsReach(t, engine, gated, 2))
	setHead(h, "B")

	current := queueGate(engine, "B")

	require.ErrorIs(t, endsAfterTicks(t, h, first).err, errSuperseded)
	waitOn(t, startsReach(t, engine, gated, 3))

	close(gated.release)

	require.NoError(t, receiveEnd(t, engine, gated, foreign).err, "a gate of another project is not a gate of the same pull request")
	require.NoError(t, receiveEnd(t, engine, gated, current).err)
	assert.EqualValues(t, 1, gated.stopped.Load())
}

func TestSuperseded_AGateThatHasNoTipIsNotStoppedByAGateThatHasOne(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	h.forge.mu.Lock()
	h.forge.err = errFake
	h.forge.mu.Unlock()

	unread := queueGate(engine, "")

	waitOn(t, startsReach(t, engine, gated, 1))

	h.forge.mu.Lock()
	h.forge.err = nil
	h.forge.state.Head = "B"
	h.forge.mu.Unlock()

	current := queueGate(engine, "")

	waitOn(t, condition(waitsInTheQueue(engine, 1)))
	// The gate of head B waits behind the gate with no tip. Nothing may stop it.
	close(gated.release)

	require.NoError(t, receiveEnd(t, engine, gated, unread).err, "a gate with no tip judges no head, so no head supersedes it")
	require.NoError(t, receiveEnd(t, engine, gated, current).err)
	assert.Zero(t, gated.stopped.Load())
}

// shiftingForge runs a hook before one read of the pull request. The test arms the hook while the gates of the engine read the forge, so mu guards the hook and the count.
type shiftingForge struct {
	*fakeForge
	mu    sync.Mutex
	reads int
	at    int
	hold  func()
}

// arm makes the nth read after this call run hold before it answers.
func (f *shiftingForge) arm(n int, hold func()) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.at = f.reads + n
	f.hold = hold
}

func (f *shiftingForge) Pull(ctx context.Context, project, item string) (review.Standing, error) {
	f.mu.Lock()
	f.reads++
	hold := f.hold
	fire := f.reads == f.at
	f.mu.Unlock()

	if fire {
		hold()
	}

	return f.fakeForge.Pull(ctx, project, item) //nolint:wrapcheck // a test double
}

func TestSuperseded_AGateWhoseReadOfTheHeadIsOldNeverStopsTheGateOfTheCurrentHead(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")

	var shifting *shiftingForge

	h, engine := supersedeEngineWith(t, gated, "B", func(fake *fakeForge) forge {
		shifting = &shiftingForge{fakeForge: fake}

		return shifting
	})

	firstRun, first := queueGateRun(engine, "A")

	waitOn(t, startsReach(t, engine, gated, 1))
	// The gate starts while the run that queued it can still be at its stop. The stop is recorded after its read, so the count is final only then.
	waitOn(t, condition(stopRecorded(engine, firstRun)))

	// The reads so far: the tip and the stop of gate A. Gate B reads its tip as read 3 and reads the head again for its stop as read 4.
	// Before that fourth read the head moves to C, and the gate of head C queues and stops the old gates.
	queuedLast := make(chan (<-chan gateEnd), 1)

	shifting.arm(2, func() {
		setHead(h, "C")

		queuedLast <- queueGate(engine, "C")

		waitOn(t, condition(func() bool {
			rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName), dbos.WithFilterStatus(dbos.WorkflowStatusCancelled))

			return err == nil && len(rows) == 2
		}))
	})

	middle := queueGate(engine, "B")

	require.ErrorIs(t, endsAfterTicks(t, h, middle).err, errSuperseded)
	require.ErrorIs(t, endsAfterTicks(t, h, first).err, errSuperseded)
	waitOn(t, startsReach(t, engine, gated, 2))

	close(gated.release)

	require.NoError(t, receiveEnd(t, engine, gated, receive(t, queuedLast)).err, "the old read of gate B did not cancel the gate of the current head")
}

// passThenHold passes the first gate and holds every later gate until its context ends.
type passThenHold struct{ runs atomic.Int32 }

func (g *passThenHold) Key(context.Context, string, string, string, string, string) (GateKey, error) {
	return GateKey{}, nil
}

func (*passThenHold) Submit(context.Context, string, string, string, string, string, string) (GateJob, error) {
	return GateJob{}, nil
}

func (g *passThenHold) Run(ctx context.Context, _, _, _ string, _ GateJob) (GateResult, error) {
	if g.runs.Add(1) == 1 {
		return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
	}

	hang(ctx.Done())

	return GateResult{}, ctx.Err()
}

const gateThenAsk = `def main(item):
    gate(name = "g")
    wait(name = "w", question = "go?")
`

func TestSuperseded_AReplayReadsTheHeadAndCancelsTheGatesNoMore(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.forge.state = review.Standing{Number: testPull, Head: "B"}
	h.gate.via = &passThenHold{}

	file := writeFlow(t, gateThenAsk)
	interrupted(t, h, file)

	// A crash left this run. An older gate of the same pull request queues while no engine runs: a replay that cancels again would cancel it.
	client := h.client(t, testVersion)
	_, err := dbos.Enqueue[any](client.flow, exclusiveQueueName, exclusiveName,
		ExclusiveInput{Kind: kindGate, Project: testProject, Item: testItem, Tip: "A", Request: CommandRequest{Project: testProject, Item: testItem}},
		dbos.WithEnqueueWorkflowID("older-gate"), dbos.WithEnqueueQueuePartitionKey(testProject))
	require.NoError(t, err)

	reads := len(h.forge.asked)
	engine := h.engine(t)
	done := runInBackground2(engine, file)

	waitOn(t, ownerAsks(t, h))
	waitOn(t, waiting[struct{}]{what: "the answer to be accepted", run: runOfClient(t, client), read: func() (struct{}, bool, error) {
		return struct{}{}, client.Reply(testItem, Reply{Text: "yes"}) == nil, nil
	}})

	require.NoError(t, receive(t, done).err)
	assert.Len(t, h.forge.asked, reads, "the replay reads the recorded head and asks the forge nothing more")

	older, err := dbos.RetrieveWorkflow[any](engine.ctx, "older-gate")
	require.NoError(t, err)

	status, err := older.GetStatus()
	require.NoError(t, err)
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, status.Status, "the recorded cancel is not run again")
}
