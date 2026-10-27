package brief_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/brief"
)

func TestParseModel_RefusesAModelThatIsInheritedOrAPlaceholder(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"", "inherit", "default", "[MODEL]", "<m>", "a b", "a\tb"} {
		t.Run(model, func(t *testing.T) {
			t.Parallel()

			_, err := brief.ParseModel("rev-1", model)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "no model for lane rev-1.")
			assert.Contains(t, err.Error(), "A dispatch with no model inherits the orchestrator's.")
		})
	}
}

func TestParseModel_AModelWithNoTierWordNeedsNoRow(t *testing.T) {
	t.Parallel()

	got, err := brief.ParseModel("rev-1", "sonnet")

	require.NoError(t, err)
	assert.Equal(t, brief.Model{Name: "sonnet", Tier: brief.TierNone}, got)
}

func TestParseModel_ASuffixOnAModelWithNoTierWordStaysInTheName(t *testing.T) {
	t.Parallel()

	got, err := brief.ParseModel("rev-1", "sonnet:subtle")

	require.NoError(t, err)
	assert.Equal(t, brief.Model{Name: "sonnet:subtle", Tier: brief.TierNone}, got)
}

func TestParseModel_OpusAndFableNeedATierRow(t *testing.T) {
	t.Parallel()

	for _, model := range []string{"opus", "fable", "opus:", "opus:cheap", "claude-opus-4"} {
		t.Run(model, func(t *testing.T) {
			t.Parallel()

			_, err := brief.ParseModel("rev-1", model)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "lane rev-1 asks for ")
			assert.Contains(t, err.Error(), " with no tier row.")
			assert.Contains(t, err.Error(), "subtle (a concurrency, migration or security change)")
		})
	}
}

func TestParseModel_SplitsTheTierRowFromTheModel(t *testing.T) {
	t.Parallel()

	cases := map[string]brief.Model{
		"opus:stuck":    {Name: "opus", Tier: brief.TierStuck},
		"opus:union":    {Name: "opus", Tier: brief.TierUnion},
		"fable:subtle":  {Name: "fable", Tier: brief.TierSubtle},
		"opus:stuck:x":  {},
		"opus-4:subtle": {Name: "opus-4", Tier: brief.TierSubtle},
	}

	for model, want := range cases {
		got, err := brief.ParseModel("fix-1", model)
		if want == (brief.Model{}) {
			require.Error(t, err, model)

			continue
		}

		require.NoError(t, err, model)
		assert.Equal(t, want, got, model)
	}
}

func TestParseModel_TheRefusalNamesTheModelWithoutItsSuffix(t *testing.T) {
	t.Parallel()

	_, err := brief.ParseModel("fix-1", "opus:cheap")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "lane fix-1 asks for opus with no tier row. Use sonnet, or opus:<row>")
}

func TestParseTier_NamesTheClosedSet(t *testing.T) {
	t.Parallel()

	for _, tier := range brief.Tiers() {
		got, err := brief.ParseTier(string(tier))
		require.NoError(t, err)
		assert.Equal(t, tier, got)
	}

	for _, bad := range []string{"", "cheap", "Stuck"} {
		_, err := brief.ParseTier(bad)
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "subtle, union and stuck")
	}
}
