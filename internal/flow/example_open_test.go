package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
)

// The example opens the draft pull request of the item right after the implement step, before the gate and the first review.
func TestExample_TheDraftPullRequestIsOpenedOnceAfterTheImplementStepWithTheClosingLineAndTheBase(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.opened, 1)
	assert.Contains(t, rig.h.forge.opened[0], "|a body\n\nAddresses #12|integration|")
}

// stackedRig is the example rig of a trunk round for an item whose parent bb-4 has a pull request in the state parentState.
func stackedRig(t *testing.T, parentState string) (*agentRig, string) {
	t.Helper()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.reader = &fakeReader{parents: map[string]string{testItem: "bb-4"}}
	rig.h.forge.historyOf = map[string][]model.PriorPull{"bb-4": {{Number: 3, State: parentState, Branch: "bb-4", Base: "main"}}}
	setRoundOf(&rig.project, "base", "main")
	rig.baseAt(t, "main")

	return rig, file
}

func TestExample_AChildOfAnOpenParentInATrunkRoundSaysAddressesAndClosesNothing(t *testing.T) {
	t.Parallel()

	rig, file := stackedRig(t, "OPEN")

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.opened, 1)
	assert.Contains(t, rig.h.forge.opened[0], "|a body\n\nAddresses #12|main|bb-4", "the pull request goes into the branch of its parent, so it must not close the issue")
	assert.Empty(t, childOf(t, result).Closes, "the audit of the merge expects the issues that the body lists, which are none")
}

func TestExample_AChildWhoseParentMergedClosesTheIssueIntoTheTrunk(t *testing.T) {
	t.Parallel()

	rig, file := stackedRig(t, "MERGED")

	result, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.opened, 1)
	assert.Contains(t, rig.h.forge.opened[0], "|a body\n\nCloses #12|main|bb-4")
	assert.Equal(t, []int{12}, childOf(t, result).Closes)
}

func TestExample_ADependentItemOpensItsPullRequestIntoTheStackOfItsParent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.reader = &fakeReader{parents: map[string]string{testItem: "bb-4"}}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.opened, 1)
	assert.Contains(t, rig.h.forge.opened[0], "|bb-4")
}
