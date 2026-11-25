package agenttest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/agenttest"
)

func TestCheckPrompt_ACorrectRunnerSendsThePromptInEachWayAndStartsAfterAPrefix(t *testing.T) {
	t.Parallel()

	var r recorder

	agenttest.CheckPrompt(&r, good(), t.TempDir())

	assert.Empty(t, r.failures)
}

func TestCheckPrompt_AFolderThatCannotHoldTheFakeCLIIsAFailure(t *testing.T) {
	t.Parallel()

	var r recorder

	agenttest.CheckPrompt(&r, good(), filepath.Join(t.TempDir(), "none"))

	require.Len(t, r.failures, 1)
	assert.Contains(t, r.failures[0], "cannot write the fake program fake-cli")
}

func TestCheckPrompt_APrefixProgramThatCannotBeWrittenIsAFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "wrap"), 0o700))

	var r recorder

	agenttest.CheckPrompt(&r, good(), dir)

	require.Len(t, r.failures, 1)
	assert.Contains(t, r.failures[0], "cannot write the fake program wrap")
}

func TestCheckPrompt_ALaunchThatDoesNotStartFailsEveryMode(t *testing.T) {
	t.Parallel()

	a := good()
	a.files = map[string]string{"/outside": "x"}

	var r recorder

	agenttest.CheckPrompt(&r, a, t.TempDir())

	assert.Contains(t, r.failures[0], "a launch with the mode stdin")
	assert.Contains(t, strings.Join(r.failures, "\n"), "a launch with the mode file")
	assert.Contains(t, strings.Join(r.failures, "\n"), "a launch with a prefix")
}
