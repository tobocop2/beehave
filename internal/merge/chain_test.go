package merge_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/merge"
)

// The tests hold one link of a stack at a time, because the world fake answers for one pull request. They do not hold the three links of a stack
// in one forge: what they prove is the poll of a merged link and the refusal of a link that was pushed to.

// mergedParent runs the poll that finds a link merged at its gated head, and returns its world after the poll moved its child onto main.
// The poll writes only the base of the direct child.
func mergedParent(t *testing.T, child int, parentBranch string) *queuedWorld {
	t.Helper()

	parent := queued(t, "MERGED")
	parent.children = kids(t, parentBranch, child)
	req := queuing()
	req.Retarget = true

	result := parent.poll(req, false)

	require.Equal(t, merge.Done, result.Stop, result.Lines)
	require.Equal(t, []string{fmt.Sprintf("SetBase %d main", child)}, parent.writes)

	return parent
}

func TestStack_ForcePushOfALinkAfterItsParentMergedRefusesItAndQueuesNothing(t *testing.T) {
	t.Parallel()

	mergedParent(t, 7, "feat")

	pushed := queued(t, "OPEN")
	pushed.set(t, map[string]any{"headRefOid": moved})
	result := pushed.run(queuing())

	assert.Equal(t, merge.Refused, result.Stop)
	assert.Contains(t, result.Lines, "REFUSE: head "+moved+" is not the gated "+sha+". Re-gate.")
	assert.Empty(t, pushed.writes, "nothing is queued for the link that was pushed to")
}

func TestStack_AReadThatFailsRefusesTheLinkAndQueuesNothing(t *testing.T) {
	t.Parallel()

	for _, read := range []string{"PRView", "StackID 5", "BranchRef", "PRList"} {
		t.Run(read, func(t *testing.T) {
			t.Parallel()

			link := queued(t, "OPEN")
			link.fails[read] = true
			req := queuing()
			req.Retarget = true
			result := link.run(req)

			assert.Equal(t, merge.Refused, result.Stop)
			assert.NotContains(t, link.writes, fmt.Sprintf("AutoMergePR %d %s", pr, sha), "a failed read never queues the merge")
		})
	}
}
