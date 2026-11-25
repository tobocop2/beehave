package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestItemBranch_TheBranchOfAnItemIsItsId(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"12", "bh-12", "Fix-Login", "proj-a1b.2"} {
		assert.Equal(t, id, model.ItemBranch(id))
	}
}
