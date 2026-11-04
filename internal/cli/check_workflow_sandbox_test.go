package cli_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
)

const boxedLocalProject = "project(repo = \"o/n\", tracker = \"github\", forge = \"github\", claims = \"beads\", beads_prefix = \"p\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\", sandbox = \"on\")\n"

func TestCheckWorkflow_AGateCallIsRefusedForAProjectWithTheSandboxOnThatBuildsHere(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    gate(name = \"a\")\n    gate(run = [\"true\"], name = \"b\")\n")
	writeConfig(t, dir, "projects/near.star", boxedLocalProject)

	code, _, stderr := runCheckWorkflow(t, "--config", dir, "--project", "near", filepath.Join(dir, "workflow.star"))

	require.Equal(t, 1, code)
	require.Contains(t, stderr, "workflow.star:2:9: ")
	require.Contains(t, stderr, "workflow.star:3:9: ")
	require.Contains(t, stderr, config.GateSandboxRefusal)
	require.Contains(t, stderr, "2 problems in")
}

func TestCheckWorkflow_AGateCallPassesWhenTheSandboxIsOff(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "workflow.star", "def main(item):\n    gate(name = \"a\")\n")
	writeConfig(t, dir, "projects/near.star", "project(repo = \"o/n\", tracker = \"github\", forge = \"github\", claims = \"beads\", beads_prefix = \"p\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n")

	code, _, stderr := runCheckWorkflow(t, "--config", dir, "--project", "near", filepath.Join(dir, "workflow.star"))

	require.Equal(t, 0, code)
	require.Empty(t, stderr)
}

func TestCheckConfig_TheNoteForASandboxedLocalProjectNamesTheProgramGates(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.star", validRunners)
	writeConfig(t, dir, "projects/near.star", boxedLocalProject)

	_, _, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Contains(t, stderr, "a gate call that names run")
	require.Contains(t, stderr, "batch_merge")
}
