package remediation_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/model"
	"github.com/tobocop2/beehest/internal/workflow/workflowtest"
)

func TestExample_TheForbidListNamesOnlyKindsOfTheEngineAndKeepsTheGitAuthorKind(t *testing.T) {
	t.Parallel()

	text, err := os.ReadFile("workflow.star")
	require.NoError(t, err)

	kinds, err := workflowtest.Constant("workflow.star", text, "FORBID")
	require.NoError(t, err)
	require.NotEmpty(t, kinds)

	known := model.FindingKindNames()

	for _, kind := range kinds {
		assert.Contains(t, known, kind, "FORBID names a kind that the engine does not know")
	}

	assert.Contains(t, kinds, string(model.FindingGitAuthor), "the row S1-L-94 needs the kind git-author")
	require.Contains(t, string(text), "forbid_commands = FORBID")
	require.Contains(t, string(text), "protect_paths = protected(round_settings)", "the kind protected-path needs the list of the step")
	require.Contains(t, string(text), "protect = protected(round_settings)", "the review loop gets the same list")
}
