package remediation_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/cli"
	"github.com/tobocop2/beehave/internal/config"
)

func TestCheckWorkflow_ExitsZeroForTheRemediationExample(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer

	code := cli.Execute(context.Background(), []string{"check", "workflow", "workflow.star"}, cli.BuildInfo{Registry: config.Adapters{}, Version: "x"}, map[string]string{}, &stdout, &stderr)

	require.Equal(t, 0, code, stderr.String())
	require.Empty(t, stdout.String())
	require.Empty(t, stderr.String())
}
