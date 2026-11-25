package remediation_test

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/closing"
	"github.com/tobocop2/beehave/internal/workflow/workflowtest"
)

// loadedPatterns reads the `forbid_text` patterns of the batch_merge call of the remediation example once.
var loadedPatterns = sync.OnceValues(func() ([]string, error) {
	text, err := os.ReadFile("workflow.star")
	if err != nil {
		return nil, err
	}

	listed, err := workflowtest.CallsTo("workflow.star", text, "batch_merge")
	if err != nil {
		return nil, err
	}

	patterns, err := workflowtest.Constant("workflow.star", text, "ATTRIBUTION")
	if err != nil {
		return nil, err
	}

	if len(listed) != 1 {
		return nil, fmt.Errorf("the example has %d batch_merge calls, and the patterns are read from one", len(listed))
	}

	patterns = append(patterns, workflowtest.Literals(listed[0].Args["forbid_text"])...)

	return patterns, nil
})

func TestAttribution_TheExampleForbidsTheFourAttributionMarks(t *testing.T) {
	t.Parallel()

	patterns, err := loadedPatterns()

	require.NoError(t, err)
	assert.Equal(t,
		[]string{"(?i)noreply@anthropic", "(?i)laude-Session", "(?i)uthored-By: Claude", "(?i)Generated with \\[Claude"},
		patterns)
}

func TestExample_ClosingKeywordsMatchTheGrammarOfTheMerge(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile("workflow.star")
	require.NoError(t, err)

	patterns, err := workflowtest.Constant("workflow.star", text, "CLOSING_KEYWORDS")
	require.NoError(t, err)
	require.Len(t, patterns, 1)

	rule := regexp.MustCompile(patterns[0])
	words := []string{"close", "Closes", "CLOSED", "fix", "fixes", "Fixed", "resolve", "resolves", "resolved", "closing", "unfixed", "prefix", "address", "fixes:", "Fixes:"}
	refs := []string{"#12", "o/r#12", "o.x/r-y_z#7", "https://github.com/o/r/issues/12", "http://github.com/o/r/issues/3", "#", "12", "other#1", "o/r/s#4", "https://example.com/o/r/issues/1"}
	joins := []string{" ", "  ", "\t", ": ", "", "-"}
	frames := []string{"%s", "see %s now", "(%s)", "x%s", "Part of #3. %s", "and %s, %s"}

	checked := 0

	for _, word := range words {
		for _, join := range joins {
			for _, ref := range refs {
				for _, frame := range frames {
					line := strings.ReplaceAll(frame, "%s", word+join+ref)
					want := len(closing.References(line)) > 0

					assert.Equal(t, want, rule.MatchString(line), "%q", line)

					checked++
				}
			}
		}
	}

	assert.Greater(t, checked, 1000)
}
