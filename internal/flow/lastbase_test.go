package flow

import (
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const restartedAgentFlow = "def main(item):\n    agent(name = \"a\", prompt = \"prompts/a.md\", model = \"m\")\n"

func TestWithBaseOf_TakesTheBaseOfTheLastRunOfTheSameItemAndKeepsNoneForAnItemWithNoRun(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	client := harness.client(t, testVersion)
	enqueueRun(t, client, runID(testItem, 1), ItemInput{Item: testItem, Project: testProject, Base: "old"}, testVersion)
	enqueueRun(t, client, runID(testItem, 2), ItemInput{Item: testItem, Project: testProject, Base: "integration/s1"}, testVersion)
	enqueueRun(t, client, runID("round/other", 1), ItemInput{Item: "round/other", Project: testProject}, testVersion)

	last, found, err := latestRun(viewOf(engine.ctx, engine.dir), testItem)
	require.NoError(t, err)

	same, err := ItemInput{Item: testItem, Project: testProject}.withBaseOf(last, found)
	require.NoError(t, err)
	assert.Equal(t, "integration/s1", same.Base, "the newest attempt of the item gives the base")

	none, found, err := latestRun(viewOf(engine.ctx, engine.dir), "13")
	require.NoError(t, err)

	other, err := ItemInput{Item: "13", Project: testProject}.withBaseOf(none, found)
	require.NoError(t, err)
	assert.Empty(t, other.Base, "an item with no run takes the base branch of the project, whatever round the project has")
}

func TestWithBaseOf_ACanceledRunWhoseStoredInputIsNotAnItemInputRefusesTheNewAttemptOnBothPaths(t *testing.T) {
	t.Parallel()

	harness := newHarness(t)
	engine := idleEngine(t, harness)
	client := harness.client(t, testVersion)
	enqueueRun(t, client, runID(testItem, 1), "not an item input", testVersion)
	require.NoError(t, dbos.CancelWorkflow(engine.ctx, runID(testItem, 1)))
	file := writeFlow(t, bodyFlow(joinFlow))

	_, err := engine.RunFile(testItem, testProject, file, modeOff())
	require.ErrorContains(t, err, "read the input of run "+runID(testItem, 1))

	_, err = client.JoinRun(t.Context(), testItem, testProject, file, modeOff())
	require.ErrorContains(t, err, "read the input of run "+runID(testItem, 1))
}

func TestRunFile_ARestartedChildOfARoundCutsItsWorktreeAtTheBaseThatItsSpawnGave(t *testing.T) {
	t.Parallel()

	rig := newAgentRig(t)
	bases := &ensureLog{}
	rig.h.trees = &fakeTrees{dir: rig.dir, bases: bases}
	engine := rig.h.engine(t)

	_, err := engine.RunFile(testRound, testProject, flowFile(t, childFlowOnBase("integration/s1"), "Probe.\n"), modeOff())
	require.NoError(t, err)

	_, err = engine.RunFile("21", testProject, flowFile(t, restartedAgentFlow, "Probe.\n"), modeOff())
	require.NoError(t, err)

	assert.Equal(t, []string{"", "integration/s1", "integration/s1"}, bases.all(), "the round tree, the child, and the restarted child")
}
