package flow

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/review"
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

	eventually(t, startedCount(gated, 1))

	// The other item has its own pull request and its own tip. It waits behind the first gate in the queue of the project.
	other := queueGateOf(engine, testProject, "99", "X")

	eventually(t, waitsInTheQueue(engine, 1))
	setHead(h, "B")

	current := queueGate(engine, "B")

	require.ErrorIs(t, endsAfterTicks(t, h, first).err, errSuperseded)

	// The cancel ends the run before the gate code sees its context end, so the count is read once the stop arrives.
	eventually(t, func() bool { return gated.stopped.Load() >= 1 })

	close(gated.release)

	require.NoError(t, (<-other).err, "a gate of another item is not a gate of the same pull request")
	require.NoError(t, (<-current).err)
	assert.EqualValues(t, 1, gated.stopped.Load(), "only the gate of the same item stopped")
}

func TestSuperseded_AGateOfAnotherProjectIsNotStopped(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h, engine := supersedeEngine(t, gated, "A")

	first := queueGate(engine, "A")
	foreign := queueGateOf(engine, "other-project", testItem, "X")

	eventually(t, startedCount(gated, 2))
	setHead(h, "B")

	current := queueGate(engine, "B")

	require.ErrorIs(t, endsAfterTicks(t, h, first).err, errSuperseded)
	eventually(t, startedCount(gated, 3))

	close(gated.release)

	require.NoError(t, (<-foreign).err, "a gate of another project is not a gate of the same pull request")
	require.NoError(t, (<-current).err)
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

	eventually(t, startedCount(gated, 1))

	h.forge.mu.Lock()
	h.forge.err = nil
	h.forge.state.Head = "B"
	h.forge.mu.Unlock()

	current := queueGate(engine, "")

	eventually(t, waitsInTheQueue(engine, 1))
	// The gate of head B waits behind the gate with no tip. Nothing may stop it.
	close(gated.release)

	require.NoError(t, (<-unread).err, "a gate with no tip judges no head, so no head supersedes it")
	require.NoError(t, (<-current).err)
	assert.Zero(t, gated.stopped.Load())
}

// shiftingForge moves the head of the pull request to a new head at the read that the test names, after it holds that read until the test lets it go.
type shiftingForge struct {
	*fakeForge
	reads atomic.Int32
	at    int32
	hold  func()
}

func (f *shiftingForge) Pull(ctx context.Context, project, item string) (review.Standing, error) {
	if f.reads.Add(1) == f.at {
		f.hold()
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

	first := queueGate(engine, "A")

	eventually(t, startedCount(gated, 1))

	// The reads so far: the tip and the stop of gate A. Gate B reads its tip as read 3 and reads the head again for its stop as read 4.
	// Before that fourth read the head moves to C, and the gate of head C queues and stops the old gates.
	var last <-chan gateEnd

	shifting.at = shifting.reads.Load() + 2
	shifting.hold = func() {
		setHead(h, "C")

		last = queueGate(engine, "C")

		eventually(t, func() bool {
			rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterName(exclusiveName), dbos.WithFilterStatus(dbos.WorkflowStatusCancelled))

			return err == nil && len(rows) == 2
		})
	}

	middle := queueGate(engine, "B")

	require.ErrorIs(t, endsAfterTicks(t, h, middle).err, errSuperseded)
	require.ErrorIs(t, endsAfterTicks(t, h, first).err, errSuperseded)
	eventually(t, startedCount(gated, 2))

	close(gated.release)

	require.NoError(t, (<-last).err, "the old read of gate B did not cancel the gate of the current head")
}

// passThenHold passes the first gate and holds every later gate until its context ends.
type passThenHold struct{ runs atomic.Int32 }

func (g *passThenHold) Key(context.Context, string, string) (GateKey, error) { return GateKey{}, nil }

func (g *passThenHold) Run(ctx context.Context, _, _ string) (GateResult, error) {
	if g.runs.Add(1) == 1 {
		return GateResult{Passed: true, Head: gatedHead, Steps: commandStep(true)}, nil
	}

	<-ctx.Done()

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

	waitForTheOwner(t, h)
	eventually(t, func() bool { return client.Answer(testItem, "yes") == nil })

	require.NoError(t, (<-done).err)
	assert.Len(t, h.forge.asked, reads, "the replay reads the recorded head and asks the forge nothing more")

	older, err := dbos.RetrieveWorkflow[any](engine.ctx, "older-gate")
	require.NoError(t, err)

	status, err := older.GetStatus()
	require.NoError(t, err)
	assert.NotEqual(t, dbos.WorkflowStatusCancelled, status.Status, "the recorded cancel is not run again")
}
