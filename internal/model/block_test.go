package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestBlock_ReadsTheKeysTheCallGave(t *testing.T) {
	t.Parallel()

	block := model.NewBlock("p.star:3:5", []model.BlockEntry{model.TextEntry("user", "ci"), model.WholeEntry("port", 22)})

	user, given := block.Text("user")
	assert.True(t, given)
	assert.Equal(t, "ci", user)

	port, given := block.Whole("port")
	assert.True(t, given)
	assert.Equal(t, 22, port)

	_, given = block.Text("port")
	assert.False(t, given, "a whole-number key is no text key")

	_, given = block.Whole("user")
	assert.False(t, given, "a text key is no whole-number key")

	_, given = block.Text("zone")
	assert.False(t, given)

	assert.Equal(t, "p.star:3:5", block.At())
}

func TestBlock_ANilBlockHoldsNothing(t *testing.T) {
	t.Parallel()

	var block *model.Block

	_, text := block.Text("user")
	_, whole := block.Whole("port")

	assert.False(t, text)
	assert.False(t, whole)
	assert.Empty(t, block.At())
	assert.Equal(t, "<nil>", block.String())
}

func TestBlock_StringListsTheKeysInOrderAndLeavesOutThePosition(t *testing.T) {
	t.Parallel()

	one := model.NewBlock("a.star:1:1", []model.BlockEntry{model.WholeEntry("port", 22), model.TextEntry("user", "ci")})
	other := model.NewBlock("b.star:9:9", []model.BlockEntry{model.TextEntry("user", "ci"), model.WholeEntry("port", 22)})

	assert.Equal(t, `{port=22 user="ci"}`, one.String())
	assert.Equal(t, one.String(), other.String())
}
