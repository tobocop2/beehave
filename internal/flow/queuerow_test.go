package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// rowFieldsFlow prints the container, the author and the change request of each row.
const rowFieldsFlow = `def main(item):
    q = queue(name = "q")
    command(name = "seen", run = ["seen"] + [r.item + ":" + r.parent + ":" + r.author + ":" + str(r.changes_requested) for r in q.rows])
`

func TestQueue_ARowCarriesItsContainerAndTheAuthorAndReviewStateOfItsPullRequest(t *testing.T) {
	t.Parallel()

	child := queueItem("21", model.SeverityP1)
	child.Parent = "9"

	h, _ := queueHarness(t,
		[]model.Item{child, queueItem("22", model.SeverityP1), queueItem("23", model.SeverityP1)},
		pullBy(t, 5, "22", "someone", "REVIEW_REQUIRED"), pullBy(t, 6, "23", "owner", "CHANGES_REQUESTED"),
	)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, rowFieldsFlow))

	require.NoError(t, err)
	assert.Equal(t, []string{"21:9::False", "22::someone:False", "23::owner:True"}, seenBy(t, h),
		"the parent comes from the item, the author and the change request from its pull request")
}

func TestQueue_ARowTakesTheAuthorOfTheLowestPullRequestOfItsBranch(t *testing.T) {
	t.Parallel()

	h, _ := queueHarness(t,
		[]model.Item{queueItem("24", model.SeverityP1)},
		pullBy(t, 9, "24", "later", "CHANGES_REQUESTED"), pullBy(t, 4, "24", "first", ""),
	)

	_, err := h.engine(t).RunFile(testItem, testProject, writeFlow(t, rowFieldsFlow))

	require.NoError(t, err)
	assert.Equal(t, []string{"24::first:False"}, seenBy(t, h), "the lowest number counts, for the author and for the review state")
}
