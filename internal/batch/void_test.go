package batch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const voidReason = "the gate did not run: node is 20.1.0 and the pin is 22.0.0"

func TestRun_AVoidBatchGateStopsTheBatchWithoutASplit(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.void = voidReason

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c", "d"))

	require.ErrorIs(t, err, ErrGateVoid)
	assert.Contains(t, err.Error(), "node is 20.1.0 and the pin is 22.0.0")
	assert.Len(t, w.gateTrees, 1, "a void gate does not split, so no item is gated alone")
	assert.Empty(t, res.Rejected, "no item is blamed for a fault of the machine")
	assert.Empty(t, res.Merged)
}

func TestRun_AVoidGateOfOneItemInASplitStopsTheBatch(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.bad = func(items []string) bool { return has(items, "c") }
	w.voidFor = func(items []string) bool { return has(items, "c") && !has(items, "d") }
	w.void = voidReason

	res, err := Run(context.Background(), w.deps(), members("a", "b", "c", "d"))

	require.ErrorIs(t, err, ErrGateVoid)
	assert.Empty(t, res.Rejected, "the item with the void gate is not rejected as red")
	assert.Equal(t, []string{"a", "b"}, res.Merged, "the members that landed before the stop stay listed")
}

func TestRun_ARedBatchGateStillSplits(t *testing.T) {
	t.Parallel()

	w := newWorld()
	w.bad = func(items []string) bool { return has(items, "b") }

	res, err := Run(context.Background(), w.deps(), members("a", "b"))

	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, itemsOfOutcomes(res.Rejected))
	assert.Equal(t, []string{"a"}, res.Merged)
}
