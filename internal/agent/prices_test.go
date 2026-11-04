package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent"
)

const priceFile = `{"models": {
	"cheap": {"input": 1, "cached_input": 0.1, "output": 10},
	"no-cache": {"input": 2, "output": 8}
}}`

func TestPrices_EstimateIsDollarsFromTheTokensAndThePricePerMillion(t *testing.T) {
	t.Parallel()

	prices, err := agent.ParsePrices([]byte(priceFile))
	require.NoError(t, err)

	cases := []struct {
		name   string
		model  string
		tokens agent.Tokens
		want   float64
		ok     bool
	}{
		{"input_and_output", "cheap", agent.Tokens{Input: 1_000_000, Output: 100_000}, 2, true},
		{"cached_input_is_priced_apart", "cheap", agent.Tokens{Input: 1_000_000, CachedInput: 600_000, Output: 0}, 0.4 + 0.06, true},
		{"reasoning_is_part_of_the_output", "cheap", agent.Tokens{Output: 1_000_000, Reasoning: 400_000}, 10, true},
		{"a_model_with_no_cached_price_charges_the_input_price", "no-cache", agent.Tokens{Input: 1_000_000, CachedInput: 500_000}, 2, true},
		{"an_unknown_model_has_no_estimate", "other", agent.Tokens{Input: 1_000_000}, 0, false},
		{"an_empty_model_has_no_estimate", "", agent.Tokens{Input: 1_000_000}, 0, false},
		{"no_tokens_have_no_estimate", "cheap", agent.Tokens{}, 0, false},
		{"cached_tokens_past_the_input_never_make_a_negative_cost", "cheap", agent.Tokens{Input: 10, CachedInput: 20}, 20 * 0.1 / 1e6, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := prices.Estimate(tc.model, tc.tokens)

			assert.Equal(t, tc.ok, ok)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}
}

func TestParsePrices_RefusesAFileThatIsNotAPriceTable(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"not_json":       "no",
		"unknown_field":  `{"models":{"m":{"input":1,"output":1,"extra":2}}}`,
		"negative_price": `{"models":{"m":{"input":-1,"output":1}}}`,
		"no_output":      `{"models":{"m":{"input":1}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := agent.ParsePrices([]byte(raw))

			assert.Error(t, err)
		})
	}
}

func TestDefaultPrices_NeverGuessesAPrice(t *testing.T) {
	t.Parallel()

	_, ok := agent.DefaultPrices().Estimate("a-model-that-is-in-no-table", agent.Tokens{Input: 1})

	assert.False(t, ok, "the table never guesses a price")
}
