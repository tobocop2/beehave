package remediation_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/workflow/workflowtest"
)

func TestExample_TheForbidListIsTheListOfTheCommandHookTests(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile("workflow.star")
	require.NoError(t, err)

	kinds, err := workflowtest.Constant("workflow.star", text, "FORBID")
	require.NoError(t, err)

	// internal/cli holds the same list for its command hook tests.
	require.Equal(t, []string{"bulk-staging", "local-delete", "remote-delete", "opaque-command", "git-author", "git-bypass", "env-override", "config-read", "protected-path", "zsh-expansion", "laptop-copy"}, kinds)
	require.Contains(t, string(text), "forbid_commands = FORBID")
	require.Contains(t, string(text), "protect_paths = protected(round_settings)", "the kind protected-path needs the list of the step")
	require.Contains(t, string(text), "protect = protected(round_settings)", "the review loop gets the same list")
}
