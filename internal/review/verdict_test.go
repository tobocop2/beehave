package review_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/review"
)

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
