package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestExample_ADependentItemOpensItsPullRequestIntoTheStackOfItsParent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.reader = &fakeReader{parents: map[string]string{testItem: "bb-4"}}

	_, err := runPipeline(rig.h.engine(t), file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.opened, 1)
	assert.Contains(t, rig.h.forge.opened[0], "|bb-4")
}
