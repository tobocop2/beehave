package cli_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const gateWorkflow = "def main(item):\n    gate(name = \"check\")\n"

func TestCheckWorkflow_AGateCallIsRefusedForAProjectWithNoRepository(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", gateWorkflow)
	writeConfig(t, dir, "projects/desk.star", "project()\n")

	code, _, stderr := runCheckWorkflow(t, "--config", dir, "--project", "desk", filepath.Join(dir, "workflow.star"))

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "workflow.star:2:9: gate needs a repository, and project desk has no repository")
	require.Contains(t, stderr, "1 problem in")
}

func TestCheckWorkflow_AGateCallPassesForAProjectWithASource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", gateWorkflow)
	writeConfig(t, dir, "projects/code.star", "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"g\", test_report = \"r.xml\")\n")

	code, _, stderr := runCheckWorkflow(t, "--config", dir, "--project", "code", filepath.Join(dir, "workflow.star"))

	require.Equal(t, 0, code)
	require.Empty(t, stderr)
}

func TestCheckWorkflow_AProjectWithNoRepositoryPassesAWorkflowWithNoGitBuiltin(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    command(run = [\"true\"])\n")
	writeConfig(t, dir, "projects/desk.star", "project()\n")

	code, _, stderr := runCheckWorkflow(t, "--config", dir, "--project", "desk", filepath.Join(dir, "workflow.star"))

	require.Equal(t, 0, code)
	require.Empty(t, stderr)
}

func TestCheckWorkflow_ANamedProjectWithNoConfigFolderFindableIsAnError(t *testing.T) {
	t.Setenv("HOME", "")

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", gateWorkflow)

	code, _, stderr := runCheckWorkflow(t, "--project", "desk", filepath.Join(dir, "workflow.star"))

	require.NotEqual(t, 0, code)
	require.Contains(t, stderr, "home folder")
}

func TestCheckWorkflow_ANamedProjectThatTheConfigLacksIsAnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", gateWorkflow)
	writeConfig(t, dir, "projects/desk.star", "project()\n")

	code, _, stderr := runCheckWorkflow(t, "--config", dir, "--project", "nope", filepath.Join(dir, "workflow.star"))

	require.Equal(t, 2, code)
	require.Contains(t, stderr, `no project "nope"`)
}
