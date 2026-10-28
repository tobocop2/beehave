package merge_test

import (
	"errors"
	"os"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

// loadedPatterns reads the `forbid_text` patterns of the merge step of the remediation example once.
var loadedPatterns = sync.OnceValues(func() ([]string, error) {
	flow, err := workflow.Load(os.DirFS("../../examples/remediation"), "workflow.hcl")
	if err != nil {
		return nil, err
	}

	var patterns []string

	var failure error

	flow.Walk(func(step workflow.Step, _ int) {
		ship, ok := step.(*workflow.Merge)
		if !ok {
			return
		}

		found, err := patternsOf(ship.ForbidText)
		patterns = append(patterns, found...)
		failure = errors.Join(failure, err)
	})

	return patterns, failure
})

// patternsOf evaluates a list of string literals.
func patternsOf(expr hcl.Expression) ([]string, error) {
	items, diags := hcl.ExprList(expr)
	if diags.HasErrors() {
		return nil, diags
	}

	patterns := make([]string, 0, len(items))

	for _, item := range items {
		value, diags := item.Value(nil)
		if diags.HasErrors() {
			return nil, diags
		}

		patterns = append(patterns, value.AsString())
	}

	return patterns, nil
}

// examplePatterns is the forbidden text of the example. It is empty when the example cannot be read, and the attribution test then fails.
func examplePatterns() []string {
	patterns, err := loadedPatterns()
	if err != nil {
		return nil
	}

	return patterns
}

// compiled compiles patterns that the check of the workflow already proved to be RE2.
func compiled(patterns []string) []*regexp.Regexp {
	rules := make([]*regexp.Regexp, len(patterns))

	for i, pattern := range patterns {
		rules[i] = regexp.MustCompile(pattern)
	}

	return rules
}

func TestAttribution_TheExampleForbidsTheFourAttributionMarks(t *testing.T) {
	t.Parallel()

	patterns, err := loadedPatterns()

	require.NoError(t, err)
	assert.Equal(t,
		[]string{"(?i)noreply@anthropic", "(?i)laude-Session", "(?i)uthored-By: Claude", "(?i)Generated with \\[Claude"},
		patterns)
}
