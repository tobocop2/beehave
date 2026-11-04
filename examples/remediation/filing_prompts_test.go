package remediation_test

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// filingRule is one rule of the filing step that only a prompt holds. The row is the conformance row that the rule closes.
type filingRule struct {
	row     string
	phrase  string
	prompts []string
}

func filingRules() []filingRule {
	return []filingRule{
		{"S1-F-01", "in the issue template of the repository", []string{"settle"}},
		{"S1-F-01", "`found`: how the finding was found", []string{"settle"}},
		{"S1-F-05", "Severity is operator-first", []string{"settle"}},
		{"S1-F-05", "exactly one of `P0`, `P1`, `P2` or `P3`", []string{"settle"}},
		{"S1-Q-27", "Do not assign the issue to anyone", []string{"settle"}},
		{"S1-F-08", "states the problem, the fix and the result", workLanes()},
		{"S1-F-08", "no `Phase N`", workLanes()},
		{"S1-F-18", "gets a changelog entry", workLanes()},
	}
}

func TestPrompts_HoldTheRulesOfTheFilingStepAndTheTextOfAChange(t *testing.T) {
	t.Parallel()

	folder := os.DirFS("prompts")

	for _, rule := range filingRules() {
		for _, name := range rule.prompts {
			raw, err := fs.ReadFile(folder, name+".md")
			require.NoError(t, err)

			if !strings.Contains(squash(string(raw)), rule.phrase) {
				t.Errorf("prompts/%s.md lost the phrase %q of the rule %s", name, rule.phrase, rule.row)
			}
		}
	}
}
