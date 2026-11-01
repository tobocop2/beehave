package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/workflow"
)

// repository writes the files into a new folder that holds a .git folder, so the folder is the repository, and returns the path of the main file.
func repository(t *testing.T, files map[string]string, main string) string {
	t.Helper()

	file := folder(t, files, main)
	require.NoError(t, os.Mkdir(filepath.Join(workflowRoot(file), ".git"), 0o700))

	return file
}

// workflowRoot returns the folder that the test files were written into.
func workflowRoot(file string) string { return filepath.Dir(file) }

func pinnedPaths(pins []workflow.PinnedFile) []string {
	paths := make([]string, len(pins))
	for i, p := range pins {
		paths[i] = p.Path
	}

	return paths
}

func TestPin_PinsAScriptOfTheRepositoryThatACommandNames(t *testing.T) {
	t.Parallel()

	file := repository(t, map[string]string{
		"flow.star":        "def main(item):\n    command(run = [\"./scripts/check.sh\", \"--fast\"])\n",
		"scripts/check.sh": "echo one\n",
	}, "flow.star")

	pins, err := workflow.Pin(file)

	require.NoError(t, err)
	assert.Contains(t, pinnedPaths(pins), "script//scripts/check.sh")

	_, err = workflow.Open(file, pins)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "scripts", "check.sh"), []byte("echo two\n"), 0o600))

	_, err = workflow.Open(file, pins)
	require.ErrorContains(t, err, "script//scripts/check.sh changed since the run started")
}

func TestPin_LeavesAProgramOnThePathAndAHostCommandUnpinned(t *testing.T) {
	t.Parallel()

	file := repository(t, map[string]string{
		"flow.star": "def main(item):\n    command(run = [\"git\", \"status\"])\n    command(run = [\"./remote.sh\"], host = \"big\")\n",
		"remote.sh": "echo remote\n",
	}, "flow.star")

	pins, err := workflow.Pin(file)

	require.NoError(t, err)
	assert.Equal(t, []string{"flow.star"}, pinnedPaths(pins))
}

func TestPin_LeavesAProgramThatNamesNoFileUnpinned(t *testing.T) {
	t.Parallel()

	file := repository(t, map[string]string{
		"flow.star": "def main(item):\n    command(run = [\"./built/tool\"])\n",
	}, "flow.star")

	pins, err := workflow.Pin(file)

	require.NoError(t, err)
	assert.Equal(t, []string{"flow.star"}, pinnedPaths(pins), "a path that the run makes is not a script")
}

func TestPin_RefusesAScriptThatDoesNotRead(t *testing.T) {
	t.Parallel()

	file := repository(t, map[string]string{
		"flow.star":        "def main(item):\n    command(run = [\"./scripts/check.sh\"])\n",
		"scripts/check.sh": "echo one\n",
	}, "flow.star")
	require.NoError(t, os.Chmod(filepath.Join(filepath.Dir(file), "scripts", "check.sh"), 0))

	_, err := workflow.Pin(file)

	require.ErrorContains(t, err, "read the script scripts/check.sh")
}

func TestOutsideScripts_ListsAScriptOutsideTheRepository(t *testing.T) {
	t.Parallel()

	outside := filepath.Join(t.TempDir(), "deploy.sh")
	require.NoError(t, os.WriteFile(outside, []byte("echo deploy\n"), 0o600))

	file := repository(t, map[string]string{
		"flow.star": "def main(item):\n    command(run = [\"" + outside + "\"])\n",
	}, "flow.star")

	pins, err := workflow.Pin(file)

	require.NoError(t, err)
	assert.Equal(t, []string{"flow.star"}, pinnedPaths(pins), "the host is not pinned")
	assert.Equal(t, []string{outside}, workflow.OutsideScripts(file))
}
