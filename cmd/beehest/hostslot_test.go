package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/host"
	"github.com/tobocop2/beehest/internal/model"
)

func TestPrograms_AHostProgramWithASlotClassSubmitsTheClassAndTheCountOfTheRequest(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	runs := programs{hosts: func(string, string) (host.Host, error) { return hosted, nil }}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Host: "h", Run: []string{"x"}, SlotClass: "mutation", SlotCount: 2})

	require.NoError(t, err)
	assert.Equal(t, model.Slot{Class: "mutation", Count: 2}, hosted.spec.Slot)
}

func TestProjectGate_TheHostJobOfAGateCarriesTheSlotThatTheEngineGave(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	gate := projectGate{
		projects: twoHostProject(),
		programs: programs{
			trees: treeAt("/tree"),
			local: gateRunner(&gateCalls{}, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
			hosts: (&clusterNames{}).host(hosted),
		},
		forge: forgeAt("https://example.test/repo.git"),
	}

	_, err := gate.Submit(t.Context(), "p", "1", "gpu-b", "", "", "", model.Slot{Class: model.GateClass, Count: 3})

	require.NoError(t, err)
	assert.Equal(t, model.Slot{Class: model.GateClass, Count: 3}, hosted.spec.Slot)
}

func TestPrograms_AHostProgramWithNoSlotClassSubmitsNoSlot(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	runs := programs{hosts: func(string, string) (host.Host, error) { return hosted, nil }}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Host: "h", Run: []string{"x"}})

	require.NoError(t, err)
	assert.Equal(t, model.Slot{}, hosted.spec.Slot)
}
