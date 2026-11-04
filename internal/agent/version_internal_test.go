package agent

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/exectest"
)

func cliThatPrints(t *testing.T, script string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "cli")
	require.NoError(t, exectest.Write(path, script))

	return path
}

func TestRunVersion_ReturnsWhatTheCLIPrints(t *testing.T) {
	t.Parallel()

	out, err := runVersion(t.Context(), cliThatPrints(t, "#!/bin/sh\necho \"cli 1.2.3 $*\"\n"), []string{"PATH=/usr/bin:/bin"})

	require.NoError(t, err)
	assert.Equal(t, "cli 1.2.3 --version\n", out)
}

func TestRunVersion_AFailingCLIReturnsItsExitCodeAndText(t *testing.T) {
	t.Parallel()

	_, err := runVersion(t.Context(), cliThatPrints(t, "#!/bin/sh\necho 'no login' >&2\nexit 3\n"), []string{"PATH=/usr/bin:/bin"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 3")
	assert.Contains(t, err.Error(), "no login")
}
