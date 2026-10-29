package merge_test

import (
	"os"
	"regexp"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow/workflowtest"
)

// loadedPatterns reads the `forbid_text` patterns of the merge step of the remediation example once.
var loadedPatterns = sync.OnceValues(func() ([]string, error) {
	text, err := os.ReadFile("../../examples/remediation/workflow.star")
	if err != nil {
		return nil, err
	}

	calls, err := workflowtest.Calls("workflow.star", text)
	if err != nil {
		return nil, err
	}

	var patterns []string

	for _, call := range calls {
		if call.Builtin == "merge" {
			patterns = append(patterns, workflowtest.Literals(call.Args["forbid_text"])...)
		}
	}

	return patterns, nil
})

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
