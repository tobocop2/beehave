package cli_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckWorkflow_AnAgentStepWithSandboxNoneGetsANoteAndTheExitCodeStaysZero(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "w.star")
	writeConfig(t, dir, "w.star", "def main(item):\n    agent(name = \"fix\", prompt = \"p.md\", model = \"sonnet\", sandbox = None)\n")

	code, stdout, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 0, code)
	require.Empty(t, stdout)
	require.Equal(t, "note: "+file+":2:70: sandbox is None, so the shell of this step runs with no sandbox. It can read every file of the owner and use the network.\n", stderr)
}

func TestCheckWorkflow_ANoteAndAProblemPrintTogetherAndTheProblemSetsTheExitCode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "w.star")
	writeConfig(t, dir, "w.star", "def main(item):\n    agent(name = \"a\", prompt = \"p.md\", model = \"m\", sandbox = None)\n    agent(name = \"b\", prompt = \"p.md\", model = \"m\", sandbox = \"on\")\n")

	code, _, stderr := runCheckWorkflow(t, file)

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "note: "+file+":2:")
	require.Contains(t, stderr, "sandbox must be sandbox(...) or None")
	require.Contains(t, stderr, "1 problem in "+file)
}
