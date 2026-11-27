package merge_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/merge"
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
	cases := map[string]struct {
		rules []*regexp.Regexp
		text  string
		// want is the source of the rule found. Empty means no rule matches.
		want string
	}{
		"the_first_line_that_matches_names_the_rule": {rules: rules, text: "clean\nsays two\nand one", want: "two"},
		"a_line_that_only_the_first_rule_matches":    {rules: rules, text: "clean\nsays one", want: "one"},
		"no_line_matches":         {rules: rules, text: "clean\nalso clean"},
		"no_rules_forbid_nothing": {text: "one"},
	}
	for name, one := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rule, found := merge.ForbiddenBy(one.rules, one.text)
			if one.want == "" {
				assert.False(t, found)

				return
			}

			require.True(t, found)
			assert.Equal(t, one.want, rule.String())
		})
	}
}
