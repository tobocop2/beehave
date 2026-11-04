package workflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

// The expected values were taken from the hand-written reader that literalValue replaced, run on the same sources.
func TestLiteralValue_ReadsOnlyLiteralsAndGivesTheValuesOfTheRun(t *testing.T) {
	t.Parallel()

	for source, want := range map[string]string{
		`"a"`:                   `"a"`,
		`1`:                     `1`,
		`-1`:                    `-1`,
		`- -1`:                  `1`,
		`-(-1)`:                 `1`,
		`1.5`:                   `1.5`,
		`-1.5`:                  `-1.5`,
		`0x10`:                  `16`,
		`9223372036854775808`:   `9223372036854775808`,
		`-9223372036854775808`:  `-9223372036854775808`,
		`True`:                  `True`,
		`None`:                  `None`,
		`[]`:                    `[]`,
		`()`:                    `()`,
		`{}`:                    `{}`,
		`(1,)`:                  `(1,)`,
		`[1, "a", None, [2.5]]`: `[1, "a", None, [2.5]]`,
		`{"a": 1, "b": [1, 2]}`: `{"a": 1, "b": [1, 2]}`,
		`{(1, 2): 3}`:           `{(1, 2): 3}`,
		`((1))`:                 `1`,
	} {
		expr, err := model.StarlarkFileOptions().ParseExpr("t", source, 0)
		require.NoError(t, err, source)

		value, ok := literalValue(expr)
		require.True(t, ok, source)
		assert.Equal(t, want, value.String(), source)
	}
}

func TestLiteralValue_RefusesWhatIsNotAPureLiteral(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`x`, `len`, `f(1)`, `[1, x]`, `{"a": x}`, `{x: 1}`, `[i for i in [1]]`, `{i: 1 for i in [1]}`,
		`"a" + "b"`, `1 + 2`, `1 if True else 2`, `lambda: 1`, `x.y`, `[1][0]`, `not True`, `+1`, `~1`,
		`-True`, `-None`, `-"a"`, `-[1]`, `{[1]: 2}`, `{{}: 1}`,
		`[1][0:1]`, `"abc"[1:]`,
		`{"a": 1, "a": 2}`, `{1: 1, 1.0: 2}`,
	} {
		expr, err := model.StarlarkFileOptions().ParseExpr("t", source, 0)
		require.NoError(t, err, source)

		_, ok := literalValue(expr)
		assert.False(t, ok, source)
	}
}
