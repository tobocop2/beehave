package flow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"
)

func TestItemStop_AFailedStepSetsNoStopOnTheRunAndReturnsItsError(t *testing.T) {
	t.Parallel()

	engine := idleEngine(t, newHarness(t))
	s := &script{e: engine, ctx: engine.ctx, counts: map[string]int{}, kinds: []string{"none"}}
	kwargs := []starlark.Tuple{
		{starlark.String("step"), starlark.String("claim_stop:c")},
		{starlark.String("code"), starlark.MakeInt(9)},
		{starlark.String("reason"), starlark.String("held")},
	}

	value, err := s.itemStop(starlark.NewBuiltin("item_stop", nil), nil, kwargs)

	require.ErrorIs(t, err, errUnknownStepKind, "a step kind outside the table fails the step")
	assert.Nil(t, value)
	assert.Nil(t, s.stopped, "a step that failed records no stop, so the run has none")
}
