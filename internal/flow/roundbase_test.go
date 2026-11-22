package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const roundRunPause = 20 * time.Millisecond

func TestRoundBase_IsEmptyForAProjectWithNoRoundRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)

	base, err := roundBase(engine.ctx, testProject)

	require.NoError(t, err)
	assert.Empty(t, base)
}

func TestRoundBase_IsTheBaseOfTheNewestRoundRunOfTheProjectAndSkipsTheRoundOfAnotherProject(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	client := harness.client(t, testVersion)

	enqueueRun(t, client, runID("round/old", 1), ItemInput{Item: "round/old", Project: testProject}, testVersion)
	time.Sleep(roundRunPause)
	enqueueRun(t, client, runID("round/new", 1), ItemInput{Item: "round/new", Project: testProject}, testVersion)
	time.Sleep(roundRunPause)
	enqueueRun(t, client, runID("round/other", 1), ItemInput{Item: "round/other", Project: "another"}, testVersion)

	base, err := roundBase(engine.ctx, testProject)

	require.NoError(t, err)
	assert.Equal(t, "new", base)
}

func TestRoundBase_RefusesARoundRunWhoseStoredInputIsNotAnItemInput(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	enqueueRun(t, harness.client(t, testVersion), runID("round/bad", 1), "not an item input", testVersion)

	_, err := roundBase(engine.ctx, testProject)

	require.ErrorContains(t, err, "read the input of run "+runID("round/bad", 1))
}

func TestRoundBase_AStoreThatIsGoneIsAnError(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	client, err := OpenClient(t.Context(), harness.opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = roundBase(client.flow, testProject)
	require.ErrorContains(t, err, "list the round runs of project "+testProject)

	_, err = client.JoinRun(t.Context(), testItem, testProject, writeFlow(t, bodyFlow(joinFlow)))
	require.Error(t, err)
}

func TestRun_AndJoinRunRefuseARoundRunWhoseStoredInputIsNotAnItemInput(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	client := harness.client(t, testVersion)
	enqueueRun(t, client, runID("round/bad", 1), "not an item input", testVersion)
	file := writeFlow(t, bodyFlow(joinFlow))

	_, err := engine.RunFile(testItem, testProject, file)
	require.ErrorContains(t, err, "read the input of run "+runID("round/bad", 1))

	_, err = client.JoinRun(t.Context(), testItem, testProject, file)
	require.ErrorContains(t, err, "read the input of run "+runID("round/bad", 1))
}

func TestWithRoundBase_GivesAnItemOfAUserTheBaseOfTheRoundAndLeavesARoundItemAndAnEntryAlone(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	enqueueRun(t, harness.client(t, testVersion), runID("round/main", 1), ItemInput{Item: "round/main", Project: testProject}, testVersion)

	tracker, err := ItemInput{Item: testItem, Project: testProject}.withRoundBase(engine.ctx)
	require.NoError(t, err)
	assert.Equal(t, "main", tracker.Base)

	round, err := ItemInput{Item: "round/other", Project: testProject}.withRoundBase(engine.ctx)
	require.NoError(t, err)
	assert.Empty(t, round.Base)

	entry, err := ItemInput{Item: testItem, Project: testProject, Entry: "pipeline"}.withRoundBase(engine.ctx)
	require.NoError(t, err)
	assert.Empty(t, entry.Base)
}
