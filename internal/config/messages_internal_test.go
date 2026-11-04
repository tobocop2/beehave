package config

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/syntax"
)

func TestParseKey_RefusesABadValueWithTheKeyTheReasonAndTheFix(t *testing.T) {
	t.Parallel()

	file := "p.star"
	at := syntax.MakePosition(&file, 3, 4)

	_, err := parseKey(at, "sandbox", "maybe", func(string) (string, error) { return "", errors.New("not a mode") })

	require.EqualError(t, err, "p.star:3:4: `sandbox`: not a mode. Fix the value.")
}

func TestOverlayWhole_RefusesANonNumberWithTheNameTheValueAndTheHint(t *testing.T) {
	t.Parallel()

	var into int

	err := overlayWhole(map[string]string{"X": "ab"}, "X", "for example 7.", &into)

	require.EqualError(t, err, "X=ab is not a whole number. Write digits only, for example 7.")
}

func TestApplyEnv_ExplainsEachWholeNumberVariableWithItsOwnHint(t *testing.T) {
	t.Parallel()

	const lead = "=x is not a whole number. Write digits only, "

	for name, hint := range map[string]string{
		envMaxConcurrent:       "for example 2.",
		envMaxReviewConcurrent: "for example 2.",
		envMaxTurns:            "for example 40, or 0 for no cap.",
		envReviewPoll:          "for example 60, or 0 for no poll.",
	} {
		cfg := DefaultConfig()

		require.EqualError(t, cfg.applyEnv(map[string]string{name: "x"}), name+lead+hint)
	}
}

func TestProblems_ErrorOfAnEmptyListIsEmpty(t *testing.T) {
	t.Parallel()

	assert.Empty(t, Problems(nil).Error())
	assert.Empty(t, Problems{}.Error())
}
