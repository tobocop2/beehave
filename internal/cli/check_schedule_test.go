package cli_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestCheckConfig_AnAdapterProblemDoesNotHideAScheduleWorkflowProblem(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.star", validRunners)
	writeConfig(t, dir, "flows/flow.star", "def other(item):\n    pass\n")
	writeConfig(t, dir, "projects/p.star", scheduledProject("@weekly"))

	code, stderr := runWithAdapters(t, func(context.Context, model.Project) error {
		return errors.New("no gh on the path")
	}, dir)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "no gh on the path")
	require.Contains(t, stderr, "project p, schedule bump")
}

// scheduledProject builds a project file with one schedule. The cron text is a parameter.
func scheduledProject(cron string) string {
	return "project(source = \"/srv/repo\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\", schedules = [schedule(name = \"bump\", cron = \"" + cron + "\", workflow = \"../flows/flow.star\", item = \"bump\")])\n"
}

func scheduledConfig(t *testing.T, workflow, cron string) string {
	t.Helper()

	dir := t.TempDir()
	writeConfig(t, dir, "runners.star", validRunners)
	writeConfig(t, dir, "flows/flow.star", workflow)
	writeConfig(t, dir, "projects/p.star", scheduledProject(cron))

	return dir
}

func TestCheckConfig_AScheduleWithAValidWorkflowPasses(t *testing.T) {
	t.Parallel()

	dir := scheduledConfig(t, "def main(item):\n    pass\n", "@weekly")

	code, _, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 0, code)
	require.Empty(t, stderr)
}

func TestCheckConfig_AScheduleWhoseWorkflowFailsTheWorkflowCheckIsReported(t *testing.T) {
	t.Parallel()

	dir := scheduledConfig(t, "def other(item):\n    pass\n", "@weekly")

	code, _, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "project p, schedule bump")
	require.Contains(t, stderr, filepath.Join(dir, "flows", "flow.star"))
	require.Contains(t, stderr, "main")
}

func TestCheckConfig_AScheduleWhoseWorkflowDoesNotParseIsReported(t *testing.T) {
	t.Parallel()

	dir := scheduledConfig(t, "def main(item:\n", "@weekly")

	code, _, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "project p, schedule bump")
}

func TestCheckConfig_AScheduleWithAFiveFieldCronIsReported(t *testing.T) {
	t.Parallel()

	dir := scheduledConfig(t, "def main(item):\n    pass\n", "0 9 * * 1")

	code, _, stderr := runCheckConfig(t, map[string]string{}, "--config", dir)

	require.Equal(t, 2, code)
	require.Contains(t, stderr, "`cron` is \"0 9 * * 1\"")
}
