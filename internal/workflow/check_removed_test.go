package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheck_ABuiltinThatTheAPIRemovedIsRefusedAsAnUndefinedNameAndNamed(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"item_assign": `item_assign(id = "1", assignee = "alice")`,
		"pr_assign":   `pr_assign(assignee = "alice", linked = True)`,
		"board_sync":  `board_sync(board = 3, status = "Done")`,
	}

	for removed, call := range tests {
		t.Run(removed, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    "+call+"\n")

			require.Len(t, got, 1)
			assert.Contains(t, got[0], "undefined: "+removed)
		})
	}
}

func TestCheck_AKeywordThatTheAPIRemovedIsRefusedByNameWithTheBuiltinThatTookIt(t *testing.T) {
	t.Parallel()

	tests := map[string]struct{ call, want string }{
		"sandbox.allow_write": {`sandbox(allow_write = ["out"])`, "sandbox takes no argument allow_write"},
		"preflight.foreign":   {`preflight(foreign = "review")`, "preflight takes no argument foreign"},
	}

	for removed, test := range tests {
		t.Run(removed, func(t *testing.T) {
			t.Parallel()

			got := checkText(t, "def main(item):\n    "+test.call+"\n")

			require.Len(t, got, 1)
			assert.Contains(t, got[0], test.want)
		})
	}
}

func TestCheckWorkflow_PROpenRefusesIssueAndNamesBody(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    pr_open(title = \"t\", base = \"main\", body = \"b\", issue = 12)\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "pr_open takes no argument issue. Write the closing line in body, such as Closes #12.")

	got = checkText(t, "def main(item):\n    pr_open(title = \"t\", base = \"main\", integration = True)\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "pr_open takes no argument integration. Pass the closing keywords in forbid_text for a pull request that may not close an issue.")
}

func TestCheckWorkflow_PRCommentRefusesIntegrationAndNamesForbidText(t *testing.T) {
	t.Parallel()

	got := checkText(t, "def main(item):\n    pr_comment(text = \"t\", key = \"k\", integration = True)\n")

	require.Len(t, got, 1)
	assert.Contains(t, got[0], "pr_comment takes no argument integration. Pass the closing keywords in forbid_text for a pull request that may not close an issue.")
}
