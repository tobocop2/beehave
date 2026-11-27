package workflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/workflow"
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

func TestPin_PinsTheReadBackAndTheUndoOfAWriteLikeItsRun(t *testing.T) {
	t.Parallel()

	file := repository(t, map[string]string{
		"flow.star":       "def main(item):\n    write(run = [\"make\"], read_back = [\"./scripts/look.sh\"], undo = [\"./scripts/drop.sh\", \"-f\"])\n",
		"scripts/look.sh": "echo look\n",
		"scripts/drop.sh": "echo one\n",
	}, "flow.star")

	pins, err := workflow.Pin(file)

	require.NoError(t, err)
	assert.Contains(t, pinnedPaths(pins), "script//scripts/look.sh")
	assert.Contains(t, pinnedPaths(pins), "script//scripts/drop.sh")

	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(file), "scripts", "drop.sh"), []byte("echo two\n"), 0o600))

	_, err = workflow.Open(file, pins)
	require.ErrorContains(t, err, "script//scripts/drop.sh changed since the run started")
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

func TestPin_PinsAScriptOutsideTheRepositoryThatACommandNames(t *testing.T) {
	t.Parallel()

	outside := outsideFile(t, "deploy.sh", "#!/bin/sh\necho one\n")
	file := outsideFlow(t, outside)

	pins, err := workflow.Pin(file)

	require.NoError(t, err)
	assert.Equal(t, []string{"flow.star", "outside:" + outside}, pinnedPaths(pins))

	_, err = workflow.Open(file, pins)
	require.NoError(t, err, "an outside script that did not change opens")

	require.NoError(t, os.WriteFile(outside, []byte("#!/bin/sh\necho two\n"), 0o600))

	_, err = workflow.Open(file, pins)
	require.ErrorContains(t, err, outside+" changed since the run started")
	assert.NotContains(t, err.Error(), "outside:", "the refusal shows the plain path")
}

func TestPin_PinsAScriptOutsideTheRepositoryThatARelativePathReaches(t *testing.T) {
	t.Parallel()

	outside := outsideFile(t, "deploy.sh", "#!/bin/sh\necho one\n")
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o700))

	rel, err := filepath.Rel(root, outside)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(rel, ".."), "the path leaves the repository")

	file := filepath.Join(root, "flow.star")
	require.NoError(t, os.WriteFile(file, []byte("def main(item):\n    command(run = [\""+rel+"\"])\n"), 0o600))

	pins, err := workflow.Pin(file)

	require.NoError(t, err)
	assert.Equal(t, []string{"flow.star", "outside:" + outside}, pinnedPaths(pins))
}

func TestPin_LeavesACompiledProgramOutsideTheRepositoryUnpinned(t *testing.T) {
	t.Parallel()

	outside := outsideFile(t, "tool", "\x7fELF compiled program\n")

	pins, err := workflow.Pin(outsideFlow(t, outside))

	require.NoError(t, err)
	assert.Equal(t, []string{"flow.star"}, pinnedPaths(pins), "a program that is not a script is not pinned")
}

func TestPin_LeavesAnOutsideScriptThatDoesNotReadUnpinned(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("a user with the root id reads a file of mode 0")
	}

	outside := outsideFile(t, "deploy.sh", "#!/bin/sh\necho one\n")
	require.NoError(t, os.Chmod(outside, 0))

	pins, err := workflow.Pin(outsideFlow(t, outside))

	require.NoError(t, err, "the run starts, as it did before outside scripts were pinned")
	assert.Equal(t, []string{"flow.star"}, pinnedPaths(pins))
}

func TestOpen_RefusesAnOutsideScriptThatWasDeleted(t *testing.T) {
	t.Parallel()

	outside := outsideFile(t, "deploy.sh", "#!/bin/sh\necho one\n")
	file := outsideFlow(t, outside)

	pins, err := workflow.Pin(file)
	require.NoError(t, err)
	require.NoError(t, os.Rename(outside, outside+".moved"))

	_, err = workflow.Open(file, pins)

	require.ErrorContains(t, err, "read "+outside+":")
	require.ErrorContains(t, err, "Restore the file")
}

func TestPin_PinsALinkToAScriptByTheContentOfTheTarget(t *testing.T) {
	t.Parallel()

	target := outsideFile(t, "real.sh", "#!/bin/sh\necho one\n")
	link := filepath.Join(t.TempDir(), "notify.sh")
	require.NoError(t, os.Symlink(target, link))
	file := outsideFlow(t, link)

	pins, err := workflow.Pin(file)
	require.NoError(t, err)
	assert.Equal(t, []string{"flow.star", "outside:" + link}, pinnedPaths(pins))

	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\necho two\n"), 0o600))

	_, err = workflow.Open(file, pins)
	require.ErrorContains(t, err, link+" changed since the run started")
}

func TestOpen_OpensARunThatRecordedNoOutsideScript(t *testing.T) {
	t.Parallel()

	outside := outsideFile(t, "deploy.sh", "#!/bin/sh\necho one\n")
	file := outsideFlow(t, outside)

	pins, err := workflow.Pin(file)
	require.NoError(t, err)

	older := []workflow.PinnedFile{pins[0]} // a run that an older build started recorded only the workflow file
	require.Equal(t, "flow.star", older[0].Path)

	require.NoError(t, os.WriteFile(outside, []byte("#!/bin/sh\necho two\n"), 0o600))

	_, err = workflow.Open(file, older)
	require.NoError(t, err, "no outside hash was recorded, so a change passes")
}

// outsideFile writes a file into a new folder outside every repository and returns its path.
func outsideFile(t *testing.T, name, text string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	return path
}

// outsideFlow returns a workflow file in a new repository with one command call that runs the program.
func outsideFlow(t *testing.T, program string) string {
	t.Helper()

	return repository(t, map[string]string{
		"flow.star": "def main(item):\n    command(run = [\"" + program + "\"])\n",
	}, "flow.star")
}
