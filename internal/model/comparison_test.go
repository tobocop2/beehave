package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/model"
)

func TestComparison_ABranchHoldsACommitOnlyWhenItIsAheadOrEqualAndTheCommitHasNothingOfItsOwn(t *testing.T) {
	t.Parallel()

	assert.True(t, model.Comparison{Status: "ahead", AheadBy: 3}.Contains())
	assert.True(t, model.Comparison{Status: "identical"}.Contains())
	assert.False(t, model.Comparison{Status: "behind", BehindBy: 2}.Contains())
	assert.False(t, model.Comparison{Status: "diverged", AheadBy: 1, BehindBy: 1}.Contains())
	assert.False(t, model.Comparison{Status: "ahead", AheadBy: 1, BehindBy: 1}.Contains(), "a status of ahead with a commit behind is not a landing")
	assert.False(t, model.Comparison{}.Contains(), "an empty answer proves nothing")
}

func TestComparison_PrintsBothCounts(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "diverged, ahead by 4, behind by 2", model.Comparison{Status: "diverged", AheadBy: 4, BehindBy: 2}.String())
}
