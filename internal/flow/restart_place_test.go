package flow

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/review"
)

// blockedProject launches an engine whose exclusive queue is held by a program gate of the project, so each gate queued behind it stays queued with the place
// it was given. It returns the harness, the deps, the engine and the run of the program gate.
func blockedProject(t *testing.T) (*harness, Deps, *Engine, string) {
	t.Helper()

	jobs := newSlotJobs("blocker")
	h := newHarness(t)
	h.commands.before = jobs.hold
	deps := h.deps()
	engine := engineOn(t, h, deps)

	blocker := queuedGate(t, engine, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "11", Request: CommandRequest{Project: testProject, Item: "11", Run: []string{"blocker"}}}, priorityNormal)
	require.Equal(t, "blocker", receive(t, jobs.started))

	return h, deps, engine, blocker
}

// queuedJob queues the build job of a gate on the slot queue of the host box, behind the job that holds the one slot.
func queuedJob(t *testing.T, engine *Engine, in ExclusiveInput, place uint) string {
	t.Helper()

	in.HostKey, in.SlotCount = hostKeyPrefix+"box", 1

	queue, err := engine.slotQueue(model.GateClass, 1)
	require.NoError(t, err)

	handle, err := dbos.RunWorkflow(engine.ctx, engine.hostJob, in, dbos.WithQueue(queue), dbos.WithQueuePartitionKey(in.HostKey), dbos.WithPriority(place))
	require.NoError(t, err)

	return handle.GetWorkflowID()
}

func TestRestartPlace_AQueuedBuildJobOfAGateTakesItsPlaceAgainWhenTheEngineStarts(t *testing.T) {
	t.Parallel()

	jobs := newSlotJobs("blocker")
	h := newHarness(t)
	h.commands.before = jobs.hold
	deps := h.deps()
	first := engineOn(t, h, deps)

	// A build job of a gate holds the one slot of the host, so the job behind it stays queued with the place it was given.
	queuedJob(t, first, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "20", Request: CommandRequest{Project: testProject, Item: "20", Run: []string{"blocker"}}}, priorityNormal)
	require.Equal(t, "blocker", receive(t, jobs.started))

	job := queuedJob(t, first, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "21", Severity: string(model.SeverityP1)}, priorityNormal)

	require.NoError(t, first.Shutdown())

	h.forge.state = review.Standing{UnresolvedThreads: 1}

	second := engineOn(t, h, deps)

	assert.Equal(t, int(priorityPersonWaits), placeOfRun(t, second, job), "a queued build job takes the place of the current rules after the restart")
}

func TestRestartPlace_APlaceThatAPersonSetSurvivesTheRestartOfTheEngine(t *testing.T) {
	t.Parallel()

	h, deps, first, _ := blockedProject(t)

	gate := queuedGate(t, first, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "12", Severity: string(model.SeverityP1)}, priorityNormal)
	other := queuedGate(t, first, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "13", Severity: string(model.SeverityP1)}, priorityNormal)

	require.NoError(t, first.Shutdown())
	require.NoError(t, h.client(t, testVersion).SetGatePriority("12", "P0"))

	h.forge.state = review.Standing{UnresolvedThreads: 1}

	second := engineOn(t, h, deps)

	assert.Equal(t, int(priorityP0), placeOfRun(t, second, gate), "the place that a person set stays after the restart")
	assert.Equal(t, int(priorityPersonWaits), placeOfRun(t, second, other), "a gate that nobody raised takes the place of the current rules")
}

func TestWritePlaces_APlaceWhoseMarkCannotBeWrittenIsNotStored(t *testing.T) {
	t.Parallel()

	_, _, engine, _ := blockedProject(t)

	gate := queuedGate(t, engine, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "12"}, priorityNormal)

	_, err := writePlaces(engine.dir, true, nil)
	require.NoError(t, err, "the first write makes the table of marks")

	db, err := sql.Open("sqlite", filepath.Join(engine.dir, databaseFile)+"?_pragma=busy_timeout(30000)")
	require.NoError(t, err)

	defer db.Close()

	_, err = db.ExecContext(t.Context(), `CREATE TRIGGER refuse_mark BEFORE INSERT ON beehest_manual_place BEGIN SELECT RAISE(ABORT, 'no mark'); END`)
	require.NoError(t, err)

	moved, err := writePlaces(engine.dir, true, map[string]placeWrite{gate: {place: priorityP0}})

	require.ErrorContains(t, err, "mark the place as set by a person")
	assert.False(t, moved)
	assert.Equal(t, int(priorityNormal), placeOfRun(t, engine, gate), "a place with no mark would be overwritten at the next start, so it is not stored")
}

func TestWritePlaces_AStoreThatIsClosedRefusesTheWriteOfAPlace(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), databaseFile))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	moved, err := writePlace(db, true, "run", placeWrite{place: priorityP0})

	require.ErrorContains(t, err, "begin the write of the place")
	assert.False(t, moved)
}

func TestWritePlaces_AGateThatStartedKeepsItsPlace(t *testing.T) {
	t.Parallel()

	_, _, engine, blocker := blockedProject(t)

	moved, err := writePlaces(engine.dir, false, map[string]placeWrite{blocker: {place: priorityP0}})

	require.NoError(t, err)
	assert.False(t, moved, "a run that does not wait is not moved")
	assert.Equal(t, int(priorityNormal), placeOfRun(t, engine, blocker))
}

func TestWritePlaces_ABuildJobUnderAGateThatAPersonMarkedKeepsItsPlace(t *testing.T) {
	t.Parallel()

	_, _, engine, _ := blockedProject(t)

	gate := queuedGate(t, engine, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "12"}, priorityNormal)
	job := queuedGate(t, engine, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "13"}, priorityNormal)
	free := queuedGate(t, engine, ExclusiveInput{Kind: kindGate, Project: testProject, Item: "14"}, priorityNormal)

	moved, err := writePlaces(engine.dir, true, map[string]placeWrite{gate: {place: priorityP0}, "": {place: priorityP0}})
	require.NoError(t, err)
	assert.True(t, moved)

	_, err = writePlaces(engine.dir, false, map[string]placeWrite{
		job:  {place: priorityPersonWaits, parent: gate},
		free: {place: priorityPersonWaits},
	})
	require.NoError(t, err)

	assert.Equal(t, int(priorityP0), placeOfRun(t, engine, gate))
	assert.Equal(t, int(priorityNormal), placeOfRun(t, engine, job), "a job under a gate that a person marked keeps its place")
	assert.Equal(t, int(priorityPersonWaits), placeOfRun(t, engine, free), "a run that nobody marked takes the place")
}
