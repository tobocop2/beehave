package flow

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
)

func TestGateSlot_IsTheGateClassWithTheRecordedCountAndNoSlotForAGateOnThisMachine(t *testing.T) {
	t.Parallel()

	assert.Equal(t, model.Slot{Class: model.GateClass, Count: 3}, ExclusiveInput{Kind: kindGate, SlotCount: 3}.gateSlot())
	assert.Equal(t, model.Slot{}, ExclusiveInput{Kind: kindGate}.gateSlot(), "a gate that runs here recorded no count")
}

func TestGateSlot_AProjectGateOnABuildHostSubmitsTheGateClassWithTheCountOfGateSlots(t *testing.T) {
	t.Parallel()

	h := mergeHarness(t, merge.Result{Stop: merge.Done})
	h.hosts = &upHosts{started: true}
	h.slots = func(_, cluster, class string) (SlotGrant, error) {
		require.Equal(t, model.GateClass, class)

		return SlotGrant{Host: cluster, Count: 3}, nil
	}

	_, err := walkText(t, h, bodyFlow("host_up(name = \"start\", host = \"gpu-b\")\ngate(name = \"check\")\n"))

	require.NoError(t, err)
	assert.Equal(t, []model.Slot{{Class: model.GateClass, Count: 3}}, h.gate.slots)
}

func TestGateSlot_ABatchGateOnABuildHostSubmitsTheGateClassWithTheRecordedCount(t *testing.T) {
	t.Parallel()

	rig := newBatchRig(t)
	engine := rig.engine(t)

	var record ExclusiveInput

	require.NoError(t, json.Unmarshal([]byte(`{"kind":"batch_gate","project":"`+testProject+`","item":"`+testItem+`","commit":"`+rig.heads["1"]+`","host_key":"host:box","slot_count":2}`), &record))

	_, err := engine.startBatchGate(t.Context(), record)

	require.NoError(t, err)
	assert.Equal(t, []model.Slot{{Class: model.GateClass, Count: 2}}, rig.gate.submitSlots)
}

func TestGateSlot_AProgramGateOnABuildHostRunsItsHostJobInTheGateClassWithTheRecordedCount(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.engine(t).programGateOutputs(t.Context(), CommandRequest{Project: testProject, Item: testItem, Host: "box", Run: []string{"make"}}, "head", model.Slot{Class: model.GateClass, Count: 2})

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 1)
	assert.Equal(t, model.GateClass, h.commands.requests[0].SlotClass)
	assert.Equal(t, 2, h.commands.requests[0].SlotCount)
	assert.True(t, h.commands.requests[0].Gate)
}

func TestCommandSlot_ACommandWithAClassRunsWithTheCountThatTheHostGrantsItAndACommandWithNoClassWithNone(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.slots = func(_, _, class string) (SlotGrant, error) {
		require.Equal(t, "mutation", class)

		return SlotGrant{Host: "box", Count: 4}, nil
	}

	_, err := walkBody(t, h, "command(name = \"mutate\", run = [\"x\"], host = \"build\", slots = \"mutation\")\ncommand(name = \"plain\", run = [\"y\"], host = \"build\")\n")

	require.NoError(t, err)
	require.Len(t, h.commands.requests, 2)
	assert.Equal(t, "mutation", h.commands.requests[0].SlotClass)
	assert.Equal(t, 4, h.commands.requests[0].SlotCount)
	assert.Empty(t, h.commands.requests[1].SlotClass)
	assert.Zero(t, h.commands.requests[1].SlotCount)
}
