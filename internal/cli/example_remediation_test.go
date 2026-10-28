package cli_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckWorkflow_ExitsZeroForTheRemediationExample(t *testing.T) {
	t.Parallel()

	code, stdout, stderr := runCheckWorkflow(t, filepath.Join("..", "..", "examples", "remediation", "workflow.hcl"))

	require.Equal(t, 0, code, stderr)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
}
