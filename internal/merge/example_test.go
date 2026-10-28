package merge

import (
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

// engineRulePatterns splits the engine rule at its alternatives, each with the `(?i)` flag the whole rule carries.
func engineRulePatterns() []string {
	var patterns []string

	for _, alternative := range strings.Split(strings.TrimPrefix(attributionRule.String(), "(?i)"), "|") {
		patterns = append(patterns, "(?i)"+alternative)
	}

	return patterns
}

// exampleForbidText reads the `forbid_text` patterns of the merge step of the remediation example.
func exampleForbidText(t *testing.T) []string {
	t.Helper()

	flow, err := workflow.Load(os.DirFS("../../examples/remediation"), "workflow.hcl")
	require.NoError(t, err)

	var patterns []string

	flow.Walk(func(step workflow.Step, _ int) {
		ship, ok := step.(*workflow.Merge)
		if !ok {
			return
		}

		items, diags := hcl.ExprList(ship.ForbidText)
		require.False(t, diags.HasErrors(), "%v", diags)

		for _, item := range items {
			value, diags := item.Value(nil)
			require.False(t, diags.HasErrors(), "%v", diags)

			patterns = append(patterns, value.AsString())
		}
	})

	return patterns
}

func TestAttribution_TheExampleForbidTextHoldsTheSamePatternsAsTheEngineRule(t *testing.T) {
	t.Parallel()

	assert.Equal(t, engineRulePatterns(), exampleForbidText(t), "R1b deletes the engine rule, so the example must hold every pattern of it")
}
