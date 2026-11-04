package merge_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/merge"
)

func TestForbidden_CompileReadsPatternsAndNamesABadOne(t *testing.T) {
	t.Parallel()

	rules, err := merge.Compile([]string{"a+", "(?i)b"})
	require.NoError(t, err)
	assert.Len(t, rules, 2)

	_, err = merge.Compile([]string{"a", "("})
	require.ErrorContains(t, err, `the forbid_text pattern "(" is not valid`)
}

func TestForbidden_ForbiddenByFindsTheRuleOfTheFirstLineThatMatches(t *testing.T) {
	t.Parallel()

	rules := []*regexp.Regexp{regexp.MustCompile("one"), regexp.MustCompile("two")}

	rule, found := merge.ForbiddenBy(rules, "clean\nsays two\nand one")
	require.True(t, found)
	assert.Equal(t, "two", rule.String())

	_, found = merge.ForbiddenBy(rules, "clean\nalso clean")
	assert.False(t, found)

	_, found = merge.ForbiddenBy(nil, "one")
	assert.False(t, found)
}
