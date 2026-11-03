package workflow_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.starlark.net/starlark"

	"github.com/tobocop2/obee/internal/workflow"
)

func TestEnvAssignments_ReadsWhatTheRunReads(t *testing.T) {
	t.Parallel()

	pairs, err := workflow.EnvAssignments(nil)
	require.NoError(t, err)
	assert.Empty(t, pairs)

	pairs, err = workflow.EnvAssignments(dictOf(t, map[string]starlark.Value{"B": starlark.String("2"), "A": starlark.String("1")}))
	require.NoError(t, err)
	assert.Equal(t, []string{"A=1", "B=2"}, pairs)

	_, err = workflow.EnvAssignments(starlark.MakeInt(1))
	require.ErrorContains(t, err, "env must be a dict of strings")
}
