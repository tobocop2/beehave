package review_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/review"
)

func TestNeedsCommit_NoFollowUpsTurnsAnyOpenFindingIntoACommit(t *testing.T) {
	t.Parallel()

	for _, class := range []review.Class{review.Deferred, review.PreExisting, review.Minor} {
		open := []review.Finding{{Class: class, Text: "later"}}

		assert.True(t, review.NeedsCommit(open, review.Policy{NoFollowUps: true}), class)
		assert.False(t, review.NeedsCommit(open, review.Policy{}), class)
	}
}

func TestNeedsCommit_ABlockingFindingNeedsACommitUnderEveryPolicy(t *testing.T) {
	t.Parallel()

	open := []review.Finding{{Class: review.Blocking, Text: "wrong"}}

	assert.True(t, review.NeedsCommit(open, review.Policy{}))
	assert.True(t, review.NeedsCommit(open, review.Policy{NoFollowUps: true}))
}

func TestNeedsCommit_NoFindingNeedsNoCommitUnderEveryPolicy(t *testing.T) {
	t.Parallel()

	assert.False(t, review.NeedsCommit(nil, review.Policy{NoFollowUps: true}))
	assert.False(t, review.NeedsCommit(nil, review.Policy{}))
}

func TestStanding_ReasonsFailClosedWhenTheReviewedCommitIsEmpty(t *testing.T) {
	t.Parallel()

	standing := review.Standing{Head: "abc"}

	assert.Equal(t, []string{`it moved to "abc" after the review read ""`}, standing.Reasons(""))
}

// trees is the fake that reads the tree of a commit.
type trees map[string]string

func (r trees) CommitTree(_ context.Context, commit string) (string, error) {
	if tree, found := r[commit]; found {
		return tree, nil
	}

	return "", errDown
}

func (trees) TreeBlobs(context.Context, string) (map[string]string, error) { return nil, errDown }

func TestStanding_AReviewStandsAcrossARewriteWhenTheTreesAreEqual(t *testing.T) {
	t.Parallel()

	standing := review.Pull(t.Context(), cleanPulls(), 7)
	standing.Compare(t.Context(), trees{head: "t1", old: "t1"}, old)

	assert.Empty(t, standing.Reasons(old))
	assert.Equal(t, head, standing.Head, "the merge still pins the new head")
}

func TestStanding_AReviewDoesNotStandWhenTheTreesDiffer(t *testing.T) {
	t.Parallel()

	standing := review.Pull(t.Context(), cleanPulls(), 7)
	standing.Compare(t.Context(), trees{head: "t1", old: "t2"}, old)

	assert.Contains(t, standing.Reasons(old), `it moved to "`+head+`" after the review read "`+old+`"`)
}

func TestStanding_ATreeThatCannotBeReadIsAReasonNotAPass(t *testing.T) {
	t.Parallel()

	standing := review.Pull(t.Context(), cleanPulls(), 7)
	standing.Compare(t.Context(), trees{head: "t1"}, old)

	assert.Contains(t, standing.Reasons(old), "the tree of the reviewed commit could not be read")
}

func TestStanding_AHeadTreeThatCannotBeReadIsAReasonNotAPass(t *testing.T) {
	t.Parallel()

	standing := review.Pull(t.Context(), cleanPulls(), 7)
	standing.Compare(t.Context(), trees{old: "t1"}, old)

	assert.Contains(t, standing.Reasons(old), "the tree of the head could not be read")
}

func TestStanding_ComparingTheReviewedHeadReadsNoTree(t *testing.T) {
	t.Parallel()

	standing := review.Pull(t.Context(), cleanPulls(), 7)
	standing.Compare(t.Context(), trees{}, head)

	assert.Empty(t, standing.Reasons(head))
}
