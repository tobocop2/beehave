package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/model"
)

func TestEnvPairs_ListsNameAndValueInSortedOrder(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"A=1", "B=", "C=x=y"}, model.EnvPairs(map[string]string{"C": "x=y", "A": "1", "B": ""}))
	assert.Empty(t, model.EnvPairs(nil))
}
