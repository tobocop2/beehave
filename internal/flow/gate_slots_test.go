package flow

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/dbos-inc/dbos-transact-golang/dbos"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

var errNoGrant = errors.New("the host block names no gate slots")

// gateSlotEngine returns an engine whose Deps.Slots is slots, and stops it when the test ends.
func gateSlotEngine(t *testing.T, slots func(project, cluster, class string) (SlotGrant, error)) *Engine {
	t.Helper()

	h := newHarness(t)
	deps := h.deps()
	deps.Slots = slots

	engine, err := NewEngine(t.Context(), h.opts, deps)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, engine.Shutdown()) })

	return engine
}

func TestGateSlots_ABuildWithNoSlotsRunsOneGateAtATime(t *testing.T) {
	t.Parallel()

	count, err := gateSlotEngine(t, nil).gateSlots("demo", "box")

	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestGateSlots_AsksTheBuildForTheReservedClassOfTheProjectOnTheHost(t *testing.T) {
	t.Parallel()

	var asked []string

	engine := gateSlotEngine(t, func(project, cluster, class string) (SlotGrant, error) {
		asked = append(asked, project+"/"+cluster+"/"+class)

		return SlotGrant{Host: cluster, Count: 3}, nil
	})

	count, err := engine.gateSlots("demo", "box")

	require.NoError(t, err)
	assert.Equal(t, 3, count)
	assert.Equal(t, []string{"demo/box/gate"}, asked)
}

func TestGateSlots_ARefusalOfTheBuildReachesTheCaller(t *testing.T) {
	t.Parallel()

	engine := gateSlotEngine(t, func(string, string, string) (SlotGrant, error) { return SlotGrant{}, errNoGrant })

	_, err := engine.gateSlots("demo", "box")

	require.ErrorIs(t, err, errNoGrant)
}

func TestPlaceOnHost_AGateOnABuildHostRecordsTheHostAndTheCountAndAMergeRecordsNeither(t *testing.T) {
	t.Parallel()

	engine := gateSlotEngine(t, func(_, cluster, _ string) (SlotGrant, error) { return SlotGrant{Host: cluster, Count: 2}, nil })

	placed, err := engine.placeOnHost(ExclusiveInput{Kind: kindGate, Project: "demo", Item: "12", Request: CommandRequest{BuildHost: "box"}})
	require.NoError(t, err)
	assert.Equal(t, "host:box", placed.HostKey)
	assert.Equal(t, 2, placed.SlotCount)

	merged, err := engine.placeOnHost(ExclusiveInput{Kind: kindMerge, Project: "demo", Item: "12"})
	require.NoError(t, err)
	assert.Empty(t, merged.HostKey)
	assert.Zero(t, merged.SlotCount)
}

func TestPlaceOnHost_AGateWhoseHostGivesNoCountIsRefusedWithTheItemAndNoQueue(t *testing.T) {
	t.Parallel()

	engine := gateSlotEngine(t, func(string, string, string) (SlotGrant, error) { return SlotGrant{}, errNoGrant })

	_, err := engine.placeOnHost(ExclusiveInput{Kind: kindGate, Project: "demo", Item: "12", Request: CommandRequest{BuildHost: "box"}})

	require.ErrorIs(t, err, errNoGrant)
	require.ErrorContains(t, err, "queue the gate of item 12")
}

func TestExclusiveHost_AGateWhoseHostGivesNoCountEndsWithTheRefusalAndRunsNoJob(t *testing.T) {
	t.Parallel()

	gated := newSupersedeGate("")
	h := newHarness(t)
	h.slots = func(_, _, class string) (SlotGrant, error) {
		require.Equal(t, model.GateClass, class)

		return SlotGrant{}, errNoGrant
	}

	engine := launchHostEngine(t, h, gated)

	ended := receive(t, queueHostGate(engine, testProject, testItem, "A", "box"))

	require.ErrorIs(t, ended.err, errNoGrant)
	assert.Zero(t, gated.started.Load(), "no build job started")
}

func TestCommand_TheClassOfTheGatesIsRefusedAndTheBuildIsNotAsked(t *testing.T) {
	t.Parallel()

	asked := 0

	engine := gateSlotEngine(t, func(string, string, string) (SlotGrant, error) { asked++; return SlotGrant{Host: "box", Count: 1}, nil })

	_, _, err := engine.grantOf(CommandRequest{Project: "demo", SlotClass: model.GateClass})

	require.ErrorIs(t, err, errGateClass)
	assert.Zero(t, asked, "the refusal comes before the build is asked")
}

func TestReopen_AGateJobGoesBackToTheQueueOfTheGateClassAndItsCount(t *testing.T) {
	t.Parallel()

	row := func(in ExclusiveInput) dbos.WorkflowStatus {
		text, err := json.Marshal(in)
		require.NoError(t, err)

		return dbos.WorkflowStatus{Name: hostJobName, Input: string(text)}
	}

	queue, partition, err := gateRewindTarget(row(ExclusiveInput{Kind: kindGate, HostKey: hostKeyPrefix + "box", SlotCount: 4}))
	require.NoError(t, err)
	assert.Equal(t, "slots:gate:4", queue)
	assert.Equal(t, "host:box", partition)

	_, _, err = gateRewindTarget(row(ExclusiveInput{Kind: kindGate, HostKey: hostKeyPrefix + "box"}))
	require.ErrorContains(t, err, `slot class "gate"`, "a gate job queued before counts were recorded names the gate class")
	require.ErrorContains(t, err, "Run the item again from its start")
}

// failedStep wraps the refusal of a child with the step and the run, so the owner knows which step of which run to act on.
func TestFailedStep_NamesTheRunAndTheStepOfAChildThatRefuses(t *testing.T) {
	t.Parallel()

	client, err := OpenClient(t.Context(), newHarness(t).opts, ReleaseDeps{})
	require.NoError(t, err)
	require.NoError(t, client.Close())

	_, err = client.failedStep("item-1-r1", []dbos.StepInfo{{StepID: 3, StepName: "phase", ChildWorkflowID: "child"}}, nil)

	require.ErrorContains(t, err, "reopen step 3 (phase) of item-1-r1")
	require.ErrorContains(t, err, "read workflow child")
}
