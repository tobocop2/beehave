package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehest/internal/model"
)

func TestItemFields_AreIDTitleAndBody(t *testing.T) {
	t.Parallel()

	assert.Equal(t, []string{"id", "title", "body", "severity"}, model.ItemFields())
}
