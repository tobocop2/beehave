package skypilot

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestBlock_DeclaresEveryKeyOptional(t *testing.T) {
	t.Parallel()

	schema := Block()

	assert.Equal(t, "skypilot", schema.Name)
	require.Len(t, schema.Args, 3)

	for _, arg := range schema.Args {
		assert.True(t, arg.Optional, arg.Key)
	}
}

func TestDecodeBlock_ReadsTheKeysAndLeavesEveryChoiceToSkyPilotWithoutABlock(t *testing.T) {
	t.Parallel()

	assert.Equal(t, block{cloud: "gcp", region: "us-central1", instanceType: "n2-standard-8"}, decodeBlock(placement()))
	assert.Equal(t, block{}, decodeBlock(nil))
	assert.Equal(t, block{}, decodeBlock(model.NewBlock("t", nil)))
}
