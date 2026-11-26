package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/review"
)

func TestDefaultSlot_AHostJobWithNoClassAsksForTheDefaultClass(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	var asked []string

	h.slots = func(project, cluster, class string) (SlotGrant, error) {
		asked = append(asked, project+"/"+cluster+"/"+class)

		return SlotGrant{Host: "box", Count: 1}, nil
	}

	_, err := walkBody(t, h, "command(name = \"job\", run = [\"x\"], host = \"build\")\n")

	require.NoError(t, err)
	assert.Equal(t, []string{testProject + "//" + model.DefaultClass}, asked)
	require.Len(t, h.commands.requests, 1)
	assert.Equal(t, model.DefaultClass, h.commands.requests[0].SlotClass)
	assert.Equal(t, 1, h.commands.requests[0].SlotCount)
}

func TestDefaultSlot_AHostJobWithNoClassWaitsAtTheCountOfTheDefaultClass(t *testing.T) {
	t.Parallel()

	jobs := newSlotJobs("n1", "n2")
	h := newHarness(t)
	h.commands.before = jobs.hold
	h.slots = func(_, _, _ string) (SlotGrant, error) { return SlotGrant{Host: "box", Count: 1}, nil }

	engine := h.engine(t)

	first := runSlotItem(t, engine, "11", slotFlow("n1", ""))
	require.Equal(t, "n1", receive(t, jobs.started))

	second := runSlotItem(t, engine, "12", slotFlow("n2", ""))
	waitOn(t, condition(func() bool { return enqueuedJobs(t, engine) == 1 }))

	assert.Equal(t, []string{"start n1"}, jobs.order(), "the second job waits for the only slot of the default class")

	jobs.let("n1")
	require.NoError(t, receive(t, first))
	require.Equal(t, "n2", receive(t, jobs.started))
	jobs.let("n2")
	require.NoError(t, receive(t, second))
}

func TestDefaultSlot_ABuildWithNoSlotClassesLeavesAHostJobWithNoClassUnlimited(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := walkBody(t, h, "command(name = \"job\", run = [\"x\"], host = \"build\")\n")

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 1)
	assert.Empty(t, h.commands.requests[0].SlotClass)
}

func TestDefaultSlot_AHostJobWithRetriesTakesNoClass(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.slots = func(_, _, _ string) (SlotGrant, error) { return SlotGrant{Host: "box", Count: 1}, nil }

	_, err := walkBody(t, h, "command(name = \"job\", run = [\"x\"], host = \"build\", retries = 1)\n")

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 1)
	assert.Empty(t, h.commands.requests[0].SlotClass, "a call with retries runs its tries as steps and takes no slot")
}

func TestDefaultSlot_ACallWithNoHostTakesNoSlot(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.slots = func(_, _, _ string) (SlotGrant, error) { return SlotGrant{Host: "box", Count: 1}, nil }

	_, err := walkBody(t, h, "command(name = \"job\", run = [\"x\"])\n")

	require.NoError(t, err)
	assert.Empty(t, h.commands.requests[0].SlotClass)
}

// queuedGate queues a gate of the given severity on the exclusive queue of an engine that never launches, so it stays queued with the place it was given.
func queuedGate(t *testing.T, engine *Engine, in ExclusiveInput, place uint) string {
	t.Helper()

	handle, err := dbos.RunWorkflow(engine.ctx, engine.exclusive, in, dbos.WithQueue(engine.exclusiveQueue), dbos.WithQueuePartitionKey(in.Project), dbos.WithPriority(place))
	require.NoError(t, err)

	return handle.GetWorkflowID()
}

func placeOfRun(t *testing.T, engine *Engine, id string) int {
	t.Helper()

	rows, err := dbos.ListWorkflows(engine.ctx, dbos.WithFilterWorkflowIDs(id))
	require.NoError(t, err)
	require.Len(t, rows, 1)

	return rows[0].Priority
}

func TestRestartPlace_AQueuedGateTakesItsPlaceFromTheCurrentRulesWhenTheEngineStarts(t *testing.T) {
	t.Parallel()

	jobs := newSlotJobs("blocker")
	h := newHarness(t)
	h.commands.before = jobs.hold
	deps := h.deps()

	first := engineOn(t, h, deps)

	// A program gate holds the only place of the project on the exclusive queue, so the runs behind it stay queued with the place they were given.
	queuedGate(t, first, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "11", Request: CommandRequest{Project: testProject, Item: "11", Run: []string{"blocker"}}}, priorityNormal)
	require.Equal(t, "blocker", receive(t, jobs.started))

	gate := queuedGate(t, first, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "12", Severity: string(model.SeverityP1)}, priorityNormal)
	merge := queuedGate(t, first, ExclusiveInput{Kind: kindMerge, Project: testProject, Item: "13"}, priorityNormal)
	command := queuedGate(t, first, ExclusiveInput{Kind: kindCommand, Project: testProject, Item: "14"}, 0)

	require.NoError(t, first.Shutdown())

	h.forge.state = review.Standing{UnresolvedThreads: 1}

	second := engineOn(t, h, deps)

	assert.Equal(t, int(priorityPersonWaits), placeOfRun(t, second, gate), "a gate whose pull request has a waiting person takes the first place after the restart")
	assert.Equal(t, int(priorityNormal), placeOfRun(t, second, merge), "a merge keeps the normal place")
	assert.Zero(t, placeOfRun(t, second, command), "the host job of a command call has no place to recompute")

	jobs.let("blocker")
}

func TestRestartPlace_AStoreThatCannotBeReadStopsTheStartOfTheEngineBeforeTheQueuesStart(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	closed, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, closed.Shutdown())

	require.ErrorContains(t, closed.launchAndSchedule(), "list the runs that wait in a queue")
}

func TestRestartPlace_AStoreThatCannotBeReadOrWrittenStopsTheLaunchWithAnError(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	closed, err := NewEngine(t.Context(), h.opts, h.deps())
	require.NoError(t, err)
	require.NoError(t, closed.Shutdown())

	require.ErrorContains(t, closed.refreshPlaces(), "list the runs that wait in a queue")

	_, err = writePlaces(t.TempDir()+"/missing", false, map[string]placeWrite{"run": {place: priorityP0}})
	require.ErrorContains(t, err, "write the place of a queued run")

	idle := idleEngine(t, h)
	idle.dir = t.TempDir() + "/missing"

	require.NoError(t, idle.refreshPlaces(), "no queued run to move needs no write")
}
