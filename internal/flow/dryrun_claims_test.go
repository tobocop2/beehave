package flow

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDryRun_AnIssueReadFindsNoAssigneeAndAPullReadFindsNoPullRequest(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	body := "def main(item):\n" +
		"    who = issue_assignees(number = 3, name = \"a\")\n" +
		"    if len(who.assignees) != 0:\n        fail(\"the dry run found an assignee\")\n" +
		"    pull = pr_read(name = \"p\")\n" +
		"    if pull.found:\n        fail(\"the dry run found a pull request\")\n"

	require.NoError(t, DryRun(t.Context(), dryFile(t, body), "12", map[string]string{}, &out), out.String())

	assert.Contains(t, out.String(), "issue_assignees:a#1")
	assert.Contains(t, out.String(), "pr_read:p#1")
}

func TestDryRun_AReadByAnIdThatIsNoItemIsRefusedAsInTheRun(t *testing.T) {
	t.Parallel()

	for name, body := range map[string]string{
		"pr_read":   "def main(item):\n    pr_read(id = \"#12\", name = \"p\")\n",
		"preflight": "def main(item):\n    preflight(id = \"+12\", name = \"p\")\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			err := DryRun(t.Context(), dryFile(t, body), "12", map[string]string{}, &out)

			require.ErrorIs(t, err, ErrBadItem, out.String())
		})
	}
}
