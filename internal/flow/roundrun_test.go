package flow

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRound  = "round/integration/s1"
	testPassID = "round-integration/s1-r1-p1"
)

// startUnder starts a second open run of the item of the harness under an id that the caller picks, as a round starts a pass run.
func startUnder(t *testing.T, h *harness, engine *Engine, item, id string) {
	t.Helper()

	in, err := pinnedInput(item, testProject, filepath.Join(filepath.Dir(h.opts.StateDir), defaultFlowName))
	require.NoError(t, err)

	_, err = dbos.RunWorkflow(engine.ctx, engine.item, in, dbos.WithWorkflowID(id))
	require.NoError(t, err)
}

func stateOfRun(t *testing.T, client *Client, id string) RunState {
	t.Helper()

	infos, err := client.Status()
	require.NoError(t, err)

	for _, info := range infos {
		if info.ID == id {
			return info.State
		}
	}

	return "absent"
}

func TestRoundRun_ARoundItemKeepsItsSlashesOutOfTheRunIdAndTheCancelMarkerFolder(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testRound)
	waitOn(t, ownerAsks(t, harness))

	assert.Equal(t, runRunning, stateOfRun(t, client, "item-round/integration/s1-r1"), "the run id holds the round id as it is")
	require.NoError(t, client.Cancel(testRound))
	assert.Equal(t, runCancelled, stateOfRun(t, client, "item-round/integration/s1-r1"))

	markers, err := os.ReadDir(filepath.Join(harness.opts.StateDir, cancelsDir))
	require.NoError(t, err)
	require.Len(t, markers, 1, "the marker is one file, not a folder tree")
	assert.False(t, markers[0].IsDir())

	harness.tick(t)
	receive(t, done)
}

func TestRoundRun_CancelRunStopsTheOlderRunOfAnItemAndLeavesTheNewerOpen(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))
	startUnder(t, harness, engine, testItem, "item-12-r2")

	older, newer := "item-12-r1", "item-12-r2"

	pollRunState(t, client, newer, runRunning)
	require.NoError(t, client.CancelRun(older))

	assert.Equal(t, runCancelled, stateOfRun(t, client, older))
	assert.Equal(t, runRunning, stateOfRun(t, client, newer), "the newer run of the item stays open")

	require.NoError(t, client.CancelRun(newer))
	harness.tick(t)
	receive(t, done)
}

func TestRoundRun_CancelRunStopsARunUnderAPassId(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	done := runInBackground(engine, testRound)
	waitOn(t, ownerAsks(t, harness))
	startUnder(t, harness, engine, testRound, testPassID)

	pollRunState(t, client, testPassID, runRunning)
	require.NoError(t, client.CancelRun(testPassID))
	assert.Equal(t, runCancelled, stateOfRun(t, client, testPassID))

	require.NoError(t, client.CancelRun("item-round/integration/s1-r1"))
	harness.tick(t)
	receive(t, done)
}

func TestRoundRun_CancelRunRefusesAnOpenRunOfAnotherBuildAndAFinishedRunOfAnyBuild(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)
	other := harness.client(t, otherBuild)
	same := harness.client(t, testVersion)

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))
	pollRunState(t, same, "item-12-r1", runRunning)

	require.ErrorIs(t, other.CancelRun("item-12-r1"), errStranded, "an open run of another build is not stopped")
	assert.Equal(t, runRunning, stateOfRun(t, same, "item-12-r1"))

	require.NoError(t, same.CancelRun("item-12-r1"))
	harness.tick(t)
	receive(t, done)

	require.ErrorIs(t, other.CancelRun("item-12-r1"), errWrongState, "a finished run of another build has nothing to cancel")
	require.ErrorIs(t, same.CancelRun("item-12-r1"), errWrongState)
}

func TestRoundRun_CancelRunByIdRefusesARunOfAnotherBuildWithTheExactText(t *testing.T) {
	t.Parallel()

	harness, _, other := strandedHarness(t)
	same := harness.client(t, testVersion)
	run, err := other.row(testRun)
	require.NoError(t, err)

	err = other.CancelRun(testRun)

	require.EqualError(t, err, "the run belongs to another version of beehave: "+testRun+" started under version "+run.ApplicationVersion+
		" and this is "+other.version+". Start version "+testVersion+" to finish it, or run beehave resume "+testItem+
		" --on-this-build to move the run to this build from its last finished step")
	assert.Equal(t, runRunning, stateOfRun(t, same, testRun), "the refused cancel leaves the run running")
}

func TestRoundRun_CancelRunRefusesAnIdNoRunHas(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)

	err := client.CancelRun("round-nothing-r1-p1")

	require.ErrorIs(t, err, errNoRunID)
	require.ErrorContains(t, err, "round-nothing-r1-p1")
	require.ErrorContains(t, err, "beehave status")
	require.NotContains(t, err.Error(), "item has")
}

func TestRoundRun_CancelRunRefusesAnIdOfNoFormTheEngineMakes(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client := harness.client(t, testVersion)

	for _, id := range []string{"", "item-12", "item-12-r0", "item-12-r1x", "round-x-r1", "round-x-r1-p0", "round-x-p1", "phase-12-r1", "xitem-12-r1", "12"} {
		err := client.CancelRun(id)

		require.ErrorIs(t, err, errBadRunID, id)
		require.ErrorContains(t, err, "Give a run id", id)
	}

	for _, id := range []string{"item-12-r1", "item-round/integration/s1-r2", testPassID} {
		require.NotErrorIs(t, client.CancelRun(id), errBadRunID, id)
	}
}

func pollRunState(t *testing.T, client *Client, id string, want RunState) {
	t.Helper()

	waitOn(t, condition(func() bool { return stateOfRun(t, client, id) == want }))
}

// roundNamed is a workflow that registers under the pass workflow name and ends at once, as a pass run of a round does.
func roundNamed(_ dbos.Context, in ItemInput) (ItemResult, error) { return ItemResult{}, nil }

func TestRoundRun_StatusListsARunOfTheRoundWorkflowNameBesideTheItemRuns(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, roundNamed, dbos.WithWorkflowName(passName))
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	in, err := pinnedInput(testRound, testProject, filepath.Join(filepath.Dir(harness.opts.StateDir), defaultFlowName))
	require.NoError(t, err)

	handle, err := dbos.RunWorkflow(engine.ctx, roundNamed, in, dbos.WithWorkflowID(testPassID))
	require.NoError(t, err)
	_, err = handle.GetResult()
	require.NoError(t, err)

	assert.Equal(t, runDone, stateOfRun(t, harness.client(t, testVersion), testPassID))
}

func TestRoundRun_CancelRunRefusesARunOfAnotherBuildAndARunThatHasEnded(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	harness.flowIs(t, waitFlow)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)
	other := harness.client(t, "v-other")

	done := runInBackground(engine, testItem)
	waitOn(t, ownerAsks(t, harness))

	require.ErrorIs(t, other.CancelRun("item-12-r1"), errStranded)

	require.NoError(t, client.CancelRun("item-12-r1"))
	require.ErrorIs(t, client.CancelRun("item-12-r1"), errWrongState, "a run that ended has nothing to cancel")

	harness.tick(t)
	receive(t, done)
}

// A finished run is refused, and the refusal names its state.
func TestRoundRun_CancelRunRefusesARunThatFinished(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := harness.engine(t)
	client := harness.client(t, testVersion)

	_, err := engine.Run(testItem, testProject)
	require.NoError(t, err)

	err = client.CancelRun("item-12-r1")
	require.ErrorIs(t, err, errWrongState)
	require.ErrorContains(t, err, "item-12-r1 is done and has nothing to cancel")
}

func TestRoundRun_CancelRunReportsAStoreThatIsGone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	require.Error(t, client.CancelRun("item-12-r1"))
}

// blockedOnText is a workflow under the pass name whose input is not an item input and which stays open until the engine stops.
func blockedOnText(ctx dbos.Context, _ string) (string, error) {
	hang(ctx.Done())

	return "", ctx.Err()
}

func TestRoundRun_CancelRunRefusesAnOpenRunWhoseInputIsNotAnItemInput(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine, err := NewEngine(t.Context(), harness.opts, harness.deps())
	require.NoError(t, err)
	dbos.RegisterWorkflow(engine.ctx, blockedOnText, dbos.WithWorkflowName(passName))
	require.NoError(t, engine.Launch())
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	_, err = dbos.RunWorkflow(engine.ctx, blockedOnText, "text", dbos.WithWorkflowID(testPassID))
	require.NoError(t, err)

	err = harness.client(t, testVersion).CancelRun(testPassID)
	require.ErrorContains(t, err, "read the input of run "+testPassID)
}

// roundTracker is a harness whose tracker takes only issue numbers.
func roundTracker(t *testing.T) (*harness, *fakeReader) {
	t.Helper()

	h, r := readerHarness(t)
	h.items.numbersOnly = true
	r.numbersOnly = true

	return h, r
}

func TestRoundRun_TheStartOfARoundItemReadsNoFieldsFromTheTracker(t *testing.T) {
	t.Parallel()

	h, _ := roundTracker(t)

	_, err := h.engine(t).RunFile(testRound, testProject, writeFlow(t, bodyFlow(`command(run = ["main", item.id])`)), modeOff())
	require.NoError(t, err)

	assert.Equal(t, []string{"main " + testRound}, h.commands.ran(), "main gets the round id as the id of its item")
	assert.Zero(t, h.items.reads.Load(), "a round item has no tracker issue to read")
}

func TestRoundRun_ARealItemStillReadsItsFieldsFromTheTracker(t *testing.T) {
	t.Parallel()

	h, _ := roundTracker(t)

	_, err := walkBody(t, h, `command(run = ["main", item.title])`)
	require.NoError(t, err)

	assert.Equal(t, []string{"main a title"}, h.commands.ran())
	assert.Equal(t, int32(1), h.items.reads.Load())
}

func TestRoundRun_ATrackerRefusalOfARealNonNumberItemStillFailsTheRun(t *testing.T) {
	t.Parallel()

	h, _ := roundTracker(t)

	_, err := h.engine(t).RunFile("bd-7", testProject, writeFlow(t, bodyFlow(`command(run = ["main"])`)), modeOff())
	require.ErrorContains(t, err, `read the fields of item bd-7: REFUSE: "bd-7" is not an issue number`)
	assert.Empty(t, h.commands.ran())
}

func TestRoundRun_ItemReadOfARoundItemFindsNothingAndAsksNoTracker(t *testing.T) {
	t.Parallel()

	for _, of := range []string{"self", "parent"} {
		t.Run(of, func(t *testing.T) {
			t.Parallel()

			h, r := roundTracker(t)

			_, err := h.engine(t).RunFile(testRound, testProject, writeFlow(t, bodyFlow(`r = item_read(of = "`+of+`", name = "up")
command(run = [str(r.found), "[" + r.id + "]", str(len(r.fields)), str(len(r.comments))])`)), modeOff())

			require.NoError(t, err)

			assert.Equal(t, []string{"False [] 0 0"}, h.commands.ran())
			assert.Zero(t, r.reads("comments")+r.reads("parent"), "no read reached the tracker")
		})
	}
}

func TestRoundRun_SpawnOfARoundItemPlacesItWithoutATrackerRead(t *testing.T) {
	t.Parallel()

	h, _ := roundTracker(t)

	_, err := walkText(t, h, `def worker(item):
    command(name = "w", run = ["w", item.id])

def main(item):
    runs = spawn(items = ["round/next"], entry = "worker", name = "fan")
    gather(runs, name = "fan")
`)
	require.NoError(t, err)

	assert.Equal(t, []string{"w round/next"}, h.commands.ran())
	assert.Equal(t, int32(1), h.items.reads.Load(), "only the item of the run read the tracker")
}
