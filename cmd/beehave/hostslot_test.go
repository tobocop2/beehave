package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
)

func TestPrograms_AHostProgramWithASlotClassSubmitsTheClassAndTheCountOfTheRequest(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	runs := programs{hosts: func(string, string) (host.Host, error) { return hosted, nil }}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Host: "h", Run: []string{"x"}, SlotClass: "mutation", SlotCount: 2})

	assert.NoError(t, err)
	assert.Equal(t, model.Slot{Class: "mutation", Count: 2}, hosted.spec.Slot)
}

func TestPrograms_AHostProgramWithNoSlotClassSubmitsNoSlot(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	runs := programs{hosts: func(string, string) (host.Host, error) { return hosted, nil }}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Host: "h", Run: []string{"x"}})

	assert.NoError(t, err)
	assert.Equal(t, model.Slot{}, hosted.spec.Slot)
}
