package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/host"
	"github.com/tobocop2/beehest/internal/model"
)

func keyedGate(hosted *fakeHost) projectGate {
	return projectGate{
		projects: clusterProject(),
		programs: programs{
			trees: treeAt("/tree"),
			local: gateRunner(&gateCalls{}, model.CommandResult{Stdout: "abc123"}, model.CommandResult{}),
			hosts: func(string, string) (host.Host, error) { return hosted, nil },
		},
		forge: forgeAt("https://example.test/repo.git"),
	}
}

func TestProjectGate_SubmitGivesTheHostJobTheKeyOfTheStepThatSubmitsIt(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}

	_, err := keyedGate(hosted).Submit(host.WithJobKey(t.Context(), "run-1:4"), "p", "1", "", "", "", "", model.Slot{})

	require.NoError(t, err)
	assert.Equal(t, "run-1:4", hosted.spec.Key)
}

func TestProjectGate_ARunThatSubmitsTheJobItselfGivesItTheKeyOfTheStep(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}

	_, err := keyedGate(hosted).Run(host.WithJobKey(t.Context(), "run-1:5"), "p", "1", "", flow.GateJob{})

	require.NoError(t, err)
	assert.Equal(t, "run-1:5", hosted.spec.Key)
}

func TestPrograms_AHostProgramGivesTheJobTheKeyOfTheStep(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	runs := programs{hosts: func(string, string) (host.Host, error) { return hosted, nil }}

	_, err := runs.Run(host.WithJobKey(t.Context(), "run-1:6"), flow.CommandRequest{Project: "p", Item: "1", Host: "h", Run: []string{"true"}})

	require.NoError(t, err)
	assert.Equal(t, "run-1:6", hosted.spec.Key)
}

func TestPrograms_AHostProgramOutsideAStepHasNoKey(t *testing.T) {
	t.Parallel()

	hosted := &fakeHost{state: model.JobSucceeded}
	runs := programs{hosts: func(string, string) (host.Host, error) { return hosted, nil }}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Host: "h", Run: []string{"true"}})

	require.NoError(t, err)
	assert.Empty(t, hosted.spec.Key)
}
