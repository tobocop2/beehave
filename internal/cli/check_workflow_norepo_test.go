package cli_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const gateWorkflow = "def main(item):\n    gate(name = \"check\")\n"

// The projects of the gate tests set sandbox = "off": the test build has no sandbox enforcer, which refuses the local gate of a sandboxed project.
func TestCheckWorkflow_AGateCallIsRefusedForAProjectWithNoRepository(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", gateWorkflow)
	writeConfig(t, dir, "projects/desk.star", "project(sandbox = \"off\")\n")

	code, _, stderr := runCheckWorkflow(t, "--config", dir, "--project", "desk", filepath.Join(dir, "workflow.star"))

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "workflow.star:2:9: gate needs a repository, and project desk has no repository")
	require.Contains(t, stderr, "1 problem in")
}

func TestCheckWorkflow_AGateCallPassesForAProjectWithASource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", gateWorkflow)
	writeConfig(t, dir, "projects/code.star", "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"g\", test_report = \"r.xml\", sandbox = \"off\")\n")

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

const noneSandboxWorkflow = "def main(item):\n    agent(prompt = \"p\", model = \"m\", sandbox = None)\n"

func TestCheckWorkflow_ASandboxNoneStepIsAProblemForAProjectWithTheSandboxOnAndANoteForOff(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		key  string
		code int
	}{"on": {`sandbox = "on"`, 1}, "off": {`sandbox = "off"`, 0}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			writeConfig(t, dir, "workflow.star", noneSandboxWorkflow)
			writeConfig(t, dir, "projects/desk.star", "project("+test.key+")\n")

			code, _, stderr := runCheckWorkflow(t, "--config", dir, "--project", "desk", filepath.Join(dir, "workflow.star"))

			require.Equal(t, test.code, code, stderr)

			if test.code == 0 {
				require.Contains(t, stderr, "note: ")
			} else {
				require.Contains(t, stderr, "workflow.star:2:48: sandbox is None, and project desk has the sandbox on")
			}
		})
	}
}
