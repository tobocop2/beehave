package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The merge itself checks no assignee, so only the claim stop of the intake of the merge round stops an item that another login took after its review.
func TestExampleMerge_TheRoundHoldsAnItemThatAnotherLoginTookAfterItsReviewAndTheOtherItemLands(t *testing.T) {
	t.Parallel()

	rig, forge := landingRound(t, nil)
	rig.kids.onReview("1", func() {
		rig.remote.mu.Lock()
		defer rig.remote.mu.Unlock()

		rig.remote.assigned = map[string][]string{"1": {"maintainer"}}
	})

	_, err := rig.run(t)

	require.NoError(t, err)
	assert.Empty(t, forge.worlds["1"].writes, "an item that another login holds is not merged")
	assert.Equal(t, "README\nthree.txt", rig.mainFiles(t), "item 3 lands and item 1 does not")
	assert.Equal(t, []string{testProject + "|1|hold"}, heldOf(rig.writer))

	notes := notesWithKey(rig.writer, "rejected")
	require.Len(t, notes, 1, "the held item gets the stop as a comment")
	assert.Contains(t, notes[0], "assigned to 'maintainer'")
}
