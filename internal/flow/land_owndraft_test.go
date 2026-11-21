package flow

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// otherDraft is the number of a draft that the run opened, which is not the pull request that the merge reads.
const otherDraft = 99

func TestExampleMerge_TheRoundLandsAnItemWhosePullRequestIsStillTheDraftItsRunOpened(t *testing.T) {
	t.Parallel()

	rig, forge := landingRound(t, map[string]any{"isDraft": true})
	rig.h.forge.onCreate = func(item string) {
		rig.remote.mu.Lock()
		defer rig.remote.mu.Unlock()

		if item == "1" {
			rig.remote.drafts = map[string]int{"1": testPull}
		}
	}

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Contains(t, forge.said("1"), "holds the gated tree", "the merge lands the draft of the run")
	assert.NotContains(t, forge.said("1"), "is a draft", "the draft guard of the merge accepts the draft of the run")
	assert.Empty(t, heldOf(rig.writer), "the run's own draft is not held at the merge")
}

func TestExampleMerge_TheRoundHoldsAnItemWhoseDraftIsNotTheOneItsRunOpened(t *testing.T) {
	t.Parallel()

	rig, forge := landingRound(t, map[string]any{"isDraft": true})
	rig.h.forge.state.Number = otherDraft

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Contains(t, forge.said("1"), "is a draft", "the run opened #99, and the merge reads the draft #7")
	assert.Empty(t, forge.worlds["1"].writes, "a refused draft is not marked ready and not merged")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))
	assert.Equal(t, []string{fmt.Sprintf("MergePR %d %s", testPull, rig.heads["3"])}, forge.worlds["3"].writes, "item 3 lands at its pin")
}
