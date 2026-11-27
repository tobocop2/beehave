package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/cli"
)

func TestCheckGateList_TheBinaryReadsAGitHubActionsFile(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "ci.yaml")
	require.NoError(t, os.WriteFile(file, []byte("permissions: read-all\njobs:\n  ship:\n    steps:\n      - name: Test\n        run: make test\n"), 0o600))

	var out, errOut bytes.Buffer

	env := map[string]string{}
	code := cli.Execute(context.Background(), []string{"check", "gate-list", file}, buildInfo(env, "dev", cli.Flow{}), env, &out, &errOut)

	assert.Zero(t, code, errOut.String())
	assert.Equal(t, "step ship:test: make test\ncheck-gatelist: OK (steps 1, refused 0)\n", out.String())
}
