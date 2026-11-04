package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
)

func TestSpend_APlusBKeepsTheDollarsAndEveryMark(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ a, b, want agent.Spend }{
		{agent.Spend{Cost: 1}, agent.Spend{Cost: 0.5}, agent.Spend{Cost: 1.5}},
		{agent.Spend{Cost: 1}, agent.Spend{Cost: 0.5, Estimated: true}, agent.Spend{Cost: 1.5, Estimated: true}},
		{agent.Spend{Cost: 1, Estimated: true}, agent.Spend{Unknown: true}, agent.Spend{Cost: 1, Estimated: true, Unknown: true}},
		{agent.Spend{Unknown: true}, agent.Spend{Cost: 2}, agent.Spend{Cost: 2, Unknown: true}},
		{agent.Spend{}, agent.Spend{}, agent.Spend{}},
	} {
		assert.Equal(t, tc.want, tc.a.Plus(tc.b), "%+v plus %+v", tc.a, tc.b)
	}
}

func TestPrices_HasSaysWhetherTheTableHoldsTheModel(t *testing.T) {
	t.Parallel()

	prices, err := agent.ParsePrices([]byte(`{"models":{"m":{"input":1,"output":2}}}`))
	require.NoError(t, err)

	assert.True(t, prices.Has("m"))
	assert.False(t, prices.Has("other"))
	assert.False(t, prices.Has(""))
}
