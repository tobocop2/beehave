package workflow_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

func TestCheck_ChecksThePathRulesOfAnAgentStepForLiterals(t *testing.T) {
	t.Parallel()

	cases := []struct{ name, args, want string }{
		{"kind without paths", `forbid_commands = ["protected-path"]`, "forbid_commands lists protected-path, so protect_paths must list at least one directory"},
		{"kind with an empty list", `forbid_commands = ["protected-path"], protect_paths = []`, "forbid_commands lists protected-path, so protect_paths must list at least one directory"},
		{"paths without a kind", `protect_paths = ["/own"]`, "protect_paths is set, but forbid_commands lists no kind that uses it"},
		{"paths with another kind", `forbid_commands = ["git-write"], protect_paths = ["/own"]`, "protect_paths is set, but forbid_commands lists no kind that uses it"},
		{"empty path", `forbid_commands = ["protected-path"], protect_paths = ["/own", ""]`, "protect_paths holds an empty path"},
		{"empty path without a kind", `protect_paths = [""]`, "protect_paths holds an empty path"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    agent(\"p\", \"m\", "+tc.args+")\n")

			require.NotEmpty(t, got)
			assert.Contains(t, got[0], tc.want)
		})
	}
}

func TestCheck_LeavesAPathRuleToTheRunWhenASideIsNotALiteral(t *testing.T) {
	t.Parallel()

	got := checkText(t, "P = [\"/own\"]\nK = [\"protected-path\"]\n\ndef main(item):\n"+
		"    agent(\"p\", \"m\", forbid_commands = K)\n"+
		"    agent(\"p\", \"m\", protect_paths = P)\n"+
		"    agent(\"p\", \"m\", forbid_commands = [\"protected-path\"], protect_paths = P)\n"+
		"    agent(\"p\", \"m\", forbid_commands = K, protect_paths = [\"/own\"])\n"+
		"    agent(\"p\", \"m\", forbid_commands = [\"protected-path\", \"git-write\"], protect_paths = [\"/own\"])\n"+
		"    agent(\"p\", \"m\", forbid_commands = [\"git-write\"])\n"+
		"    agent(\"p\", \"m\")\n")

	assert.Empty(t, got)
}

func TestCheck_ReportsAnUnknownKindOnceAndSkipsThePathRules(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    agent(\"p\", \"m\", forbid_commands = [\"nope\"], protect_paths = [\"/own\"])\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "unknown kind")
}

func TestCheck_AcceptsTheShippedWorkflowsOnThePathRules(t *testing.T) {
	t.Parallel()

	loader := folder(t, map[string]string{
		"workflow.star": "load(\"@obee//review_fix.star\", \"review_fix\")\ndef main(item):\n    return gate(name = \"check\")\n",
	}, "workflow.star")

	for _, path := range []string{loader, filepath.Join("..", "..", "examples", "remediation", "workflow.star")} {
		found, _ := workflow.Check(path)
		assert.Empty(t, problems(found), path)
	}
}
