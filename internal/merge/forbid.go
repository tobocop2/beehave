package merge

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Compile compiles the forbid_text patterns of a step. A pattern that is not RE2 is an error that names the pattern.
func Compile(patterns []string) ([]*regexp.Regexp, error) {
	rules := make([]*regexp.Regexp, len(patterns))

	for i, pattern := range patterns {
		rule, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("the forbid_text pattern %q is not valid: %w", pattern, err)
		}

		rules[i] = rule
	}

	return rules, nil
}

// matchingRule returns the first rule that the line matches, and false when none matches. It is the one line test of the merge guard and of the posting steps.
func matchingRule(rules []*regexp.Regexp, line string) (*regexp.Regexp, bool) {
	i := slices.IndexFunc(rules, func(rule *regexp.Regexp) bool { return rule.MatchString(line) })
	if i < 0 {
		return nil, false
	}

	return rules[i], true
}

// ForbiddenBy returns the first rule that a line of the text matches, and false when no line matches.
func ForbiddenBy(rules []*regexp.Regexp, text string) (*regexp.Regexp, bool) {
	for _, line := range strings.Split(text, "\n") {
		if rule, found := matchingRule(rules, line); found {
			return rule, true
		}
	}

	return nil, false
}
