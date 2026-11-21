package flow

import (
	"strings"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const strangerFlow = `def main(item):
    done = gather([struct(item = "21", run = "item-21-r1")], name = "other")
    command(name = "after", run = ["after", done.results[0].error])
`

func TestGather_RefusesARunThatAUserStarted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	_, err := engine.RunFile("21", testProject, writeFlow(t, bodyFlow(`command(name = "w", run = ["w"])`)))
	require.NoError(t, err)

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, strangerFlow))

	require.ErrorContains(t, err, "run item-21-r1 is not a child of this run")
	assert.Empty(t, h.commands.ranBy(testItem), "the gather did not return")
}

func TestGather_RefusesARunThatAnotherRunSpawned(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	spawner := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    gather(spawn(items = [\"21\"], entry = \"worker\"))\n"
	_, err := engine.RunFile("13", testProject, writeFlow(t, spawner))
	require.NoError(t, err)

	_, err = engine.RunFile(testItem, testProject, writeFlow(t, strangerFlow))

	require.ErrorContains(t, err, "run item-21-r1 is not a child of this run")
}

func TestGather_RefusesARunIdThatNoSpawnReturned(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	flow := "def main(item):\n    gather([struct(item = \"99\", run = \"item-99-r1\")], name = \"none\")\n"
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))

	require.ErrorContains(t, err, "run item-99-r1 is not a child of this run")
}

func TestGather_RefusesAStructWhoseRunIsNotTheRunOfItsItem(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	flow := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    runs = spawn(items = [\"21\"], entry = \"worker\")\n    gather([struct(item = \"21\", run = \"item-21-r9\")], name = \"forged\")\n"
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))

	require.ErrorContains(t, err, "run item-21-r9 is not a child of this run")
}

func TestGather_AcceptsARunThatAnEarlierSpawnOfTheSameRunReturned(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	flow := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    first = spawn(items = [\"21\"], entry = \"worker\", name = \"a\")\n    second = spawn(items = [\"22\"], entry = \"worker\", name = \"b\")\n    done = gather(first.runs + second.runs, name = \"both\")\n    command(name = \"after\", run = [\"after\", str(len(done.results))])\n"
	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, flow))

	require.NoError(t, err)
	assert.Equal(t, []string{"after 2"}, h.commands.ranBy(testItem))
}

// A fork of the parent at the gather step has a new run id, and the child keeps the parent id of the first run.
// The gather of the fork must pass as it did the first time.
func TestGather_AReplayInAForkOfTheParentStillAcceptsItsChildren(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	engine := h.engine(t)

	flow := "def worker(item):\n    command(name = \"w\", run = [\"w\"])\n\ndef main(item):\n    done = gather(spawn(items = [\"21\"], entry = \"worker\"), name = \"fan\")\n    command(name = \"after\", run = [\"after\", str(done.results[0].ok)])\n"
	_, err := engine.RunFile(testItem, testProject, writeFlow(t, flow))
	require.NoError(t, err)

	steps, err := dbos.GetWorkflowSteps(engine.ctx, runID(testItem, 1))
	require.NoError(t, err)

	at := -1

	for _, step := range steps {
		if strings.HasPrefix(step.StepName, "gather") {
			at = step.StepID
		}
	}

	require.GreaterOrEqual(t, at, 0, "the parent recorded a gather step")

	forked, err := dbos.ForkWorkflow[ItemResult](engine.ctx, dbos.ForkWorkflowInput{OriginalWorkflowID: runID(testItem, 1), StartStep: uint(at)})
	require.NoError(t, err)

	_, err = forked.GetResult()

	require.NoError(t, err, "the fork replays spawn from its record and gathers the same child")
	assert.Len(t, h.commands.ranBy(testItem), 2, "the fork ran the step after the gather again")
}

func TestGather_ASpawnedRunWhoseRowIsGoneIsAFailedOutcome(t *testing.T) {
	t.Parallel()

	engine := newHarness(t).engine(t)
	out := engine.outcome(spawnedRun{Item: "21", Run: "item-21-r1"}, dbos.WorkflowStatus{}, false)

	assert.False(t, out.OK)
	assert.Equal(t, "run item-21-r1 is not in the store", out.Error)
}
