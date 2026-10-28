package model_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/model"
)

func TestFindingKinds_ListsTheSixKindsSortedByName(t *testing.T) {
	t.Parallel()

	kinds := model.FindingKinds()

	assert.Len(t, kinds, 6)
	assert.True(t, slices.IsSorted(kinds), "%v", kinds)
	assert.Contains(t, kinds, model.FindingBulkStaging)
	assert.Contains(t, kinds, model.FindingOpaque)
	assert.Equal(t, "bulk-staging", model.FindingKindNames()[0])
	assert.Len(t, model.FindingKindNames(), len(kinds))
}
