package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The example opens the draft pull request of the item right after the implement step, before the gate and the first review.
func TestExample_TheDraftPullRequestIsOpenedOnceAfterTheImplementStepWithTheIssueAndTheBase(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.opened, 1)
	assert.Contains(t, rig.h.forge.opened[0], "|12|integration|")
}

func TestExample_ADependentItemOpensItsPullRequestIntoTheStackOfItsParent(t *testing.T) {
	t.Parallel()

	rig, file := exampleRig(t, func(int) string { return "approve" })
	rig.h.opts.Env["PARENT"] = "bb-4"

	_, err := rig.h.engine(t).RunFile(testItem, testProject, file)

	require.NoError(t, err)
	require.Len(t, rig.h.forge.opened, 1)
	assert.Contains(t, rig.h.forge.opened[0], "|bb-4")
}
