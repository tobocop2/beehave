package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

// scheduleWorkflow is the text of a workflow file that a schedule names.
const scheduleWorkflow = "def main(item):\n    pass\n"

// loadSchedules loads a project whose `schedules` list is the given text. The word WORKFLOW in the text stands for a workflow file that exists.
func loadSchedules(t *testing.T, list string) (config.Project, config.Problems) {
	t.Helper()

	dir := t.TempDir()
	workflow := writeFile(t, filepath.Join(dir, "flows", "bump.star"), scheduleWorkflow)
	list = strings.ReplaceAll(list, "WORKFLOW", workflow)

	writeFile(t, filepath.Join(dir, "projects", "p.star"), "\n"+call(localProject+", schedules = "+list))
	cfg, problems := config.LoadFolder(dir, nil)

	return cfg.Projects["p"], problems
}

func oneSchedule(extra string) string {
	return `[schedule(name = "bump", cron = "0 0 9 * * 1", workflow = "WORKFLOW", item = "bump"` + extra + `)]`
}

func TestLoadFolder_AScheduleBlockLoadsWithCatchUpOffAndUTCByDefault(t *testing.T) {
	t.Parallel()

	project, problems := loadSchedules(t, oneSchedule(""))

	require.Empty(t, problems)
	require.Len(t, project.Schedules, 1)

	got := project.Schedules[0]
	require.Equal(t, model.Schedule{Name: "bump", Cron: "0 0 9 * * 1", Workflow: got.Workflow, Item: "bump", Timezone: "UTC", CatchUp: false}, got)
	require.True(t, filepath.IsAbs(got.Workflow))
}

func TestLoadFolder_AScheduleKeepsItsTimezoneAndCatchUp(t *testing.T) {
	t.Parallel()

	project, problems := loadSchedules(t, oneSchedule(`, timezone = "Europe/Berlin", catch_up = True`))

	require.Empty(t, problems)
	require.Equal(t, "Europe/Berlin", project.Schedules[0].Timezone)
	require.True(t, project.Schedules[0].CatchUp)
}

func TestLoadFolder_AProjectWithNoSchedulesHasNone(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject))

	require.Empty(t, problems)
	require.Empty(t, project.Schedules)
}

func TestLoadFolder_TheCronTextUsesTheParserOfDBOS(t *testing.T) {
	t.Parallel()

	for cron, ok := range map[string]bool{
		"0 0 9 * * 1":     true,
		"@weekly":         true,
		"@every 1h":       true,
		"0 9 * * 1":       false,
		"":                false,
		"not a cron":      false,
		"0 0 9 * * 1 2 3": false,
	} {
		_, problems := loadSchedules(t, `[schedule(name = "bump", cron = "`+cron+`", workflow = "WORKFLOW", item = "bump")]`)

		if ok {
			require.Empty(t, problems, cron)

			continue
		}

		require.NotEmpty(t, problems, cron)
		require.Contains(t, problems[0].Error(), "p.star:2:", cron)
		require.Contains(t, problems[0].Error(), "cron", cron)
	}
}

func TestLoadFolder_ACronTextWithItsOwnZonePrefixIsRefusedAndNamesTheTimezoneKey(t *testing.T) {
	t.Parallel()

	for _, cron := range []string{"TZ=Europe/Berlin 0 0 9 * * 1", "CRON_TZ=Europe/Berlin @daily"} {
		_, problems := loadSchedules(t, `[schedule(name = "bump", cron = "`+cron+`", workflow = "WORKFLOW", item = "bump")]`)

		require.NotEmpty(t, problems, cron)
		require.Contains(t, problems[0].Error(), "p.star:2:", cron)
		require.Contains(t, problems[0].Error(), "`timezone`", cron)
	}
}

// The zone prefix only sets the location, and the loader already checks the zone, so no cron text is accepted or refused because of it. This test checks that a schedule with a timezone loads.
func TestLoadFolder_AScheduleWithATimezoneLoadsWithoutProblems(t *testing.T) {
	t.Parallel()

	_, problems := loadSchedules(t, oneSchedule(`, timezone = "Europe/Berlin"`))

	require.Empty(t, problems)
}

func TestLoadFolder_ABadScheduleIsRefusedWithTheFileTheLineAndTheRule(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct{ list, rule string }{
		"an empty name":          {`[schedule(name = "", cron = "@daily", workflow = "WORKFLOW", item = "bump")]`, "Write a lower case letter"},
		"an unsafe name":         {`[schedule(name = "Bump Now", cron = "@daily", workflow = "WORKFLOW", item = "bump")]`, "Write a lower case letter"},
		"a duplicate name":       {`[schedule(name = "bump", cron = "@daily", workflow = "WORKFLOW", item = "a"), schedule(name = "bump", cron = "@weekly", workflow = "WORKFLOW", item = "b")]`, "another schedule has it"},
		"an empty item":          {`[schedule(name = "bump", cron = "@daily", workflow = "WORKFLOW", item = " ")]`, "`item` must be a non-empty string"},
		"an unsafe item":         {`[schedule(name = "bump", cron = "@daily", workflow = "WORKFLOW", item = "a/b")]`, "letters, digits and hyphens only"},
		"a bad timezone":         {oneSchedule(`, timezone = "Mars/Base"`), "`timezone` is \"Mars/Base\""},
		"an empty timezone":      {oneSchedule(`, timezone = ""`), "`timezone` must be a non-empty string"},
		"a text catch_up":        {oneSchedule(`, catch_up = "yes"`), "catch_up"},
		"an int catch_up":        {oneSchedule(`, catch_up = 1`), "catch_up"},
		"a missing workflow":     {`[schedule(name = "bump", cron = "@daily", workflow = "/nowhere/w.star", item = "bump")]`, "not a file you can read"},
		"an empty workflow":      {`[schedule(name = "bump", cron = "@daily", workflow = "", item = "bump")]`, "`workflow` must be a non-empty string"},
		"a workflow folder":      {`[schedule(name = "bump", cron = "@daily", workflow = "/", item = "bump")]`, "not a file you can read"},
		"no cron":                {`[schedule(name = "bump", workflow = "WORKFLOW", item = "bump")]`, "cron"},
		"an unknown key":         {oneSchedule(`, every = "day"`), "every"},
		"a text instead of list": {`"bump"`, "schedules"},
		"a list of texts":        {`["bump"]`, "schedule"},
		"a project block":        {`[project()]`, "schedule"},
	} {
		_, problems := loadSchedules(t, test.list)

		require.NotEmpty(t, problems, name)
		require.Contains(t, problems[0].Error(), "p.star:2:", name)
		require.Contains(t, problems[0].Error(), test.rule, name)
	}
}

func TestLoadFolder_AScheduleWithARelativeWorkflowReadsItFromTheFolderOfTheProjectFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "flows", "bump.star"), scheduleWorkflow)
	writeFile(t, filepath.Join(dir, "projects", "p.star"), call(localProject+`, schedules = [schedule(name = "bump", cron = "@daily", workflow = "../flows/bump.star", item = "bump")]`))

	cfg, problems := config.LoadFolder(dir, nil)

	require.Empty(t, problems)
	require.Equal(t, filepath.Join(dir, "flows", "bump.star"), cfg.Projects["p"].Schedules[0].Workflow)
}

func TestLoadFolder_AScheduleWorkflowStartingWithTheHomeMarkExpandsToTheHome(t *testing.T) { //nolint:paralleltest // sets HOME
	home := homeDir(t)

	writeFile(t, filepath.Join(home, "w.star"), scheduleWorkflow)

	project, problems := loadProjectText(t, call(localProject+`, schedules = [schedule(name = "bump", cron = "@daily", workflow = "~/w.star", item = "bump")]`))

	require.Empty(t, problems)
	require.Equal(t, filepath.Join(home, "w.star"), project.Schedules[0].Workflow)
}

func TestLoadFolder_AScheduleWorkflowWithTheHomeMarkIsRefusedWhenTheHomeIsUnknown(t *testing.T) {
	t.Setenv("HOME", "")

	_, problems := loadProjectText(t, call(localProject+`, schedules = [schedule(name = "bump", cron = "@daily", workflow = "~/w.star", item = "bump")]`))

	require.NotEmpty(t, problems)
	require.Contains(t, problems[0].Error(), "HOME")
}

func TestLoadFolder_AScheduleThatNoProjectHoldsIsRefused(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "_s = schedule(name = \"a\", cron = \"@daily\", workflow = \"/etc/hosts\", item = \"a\")\n"+call(localProject))

	require.NotEmpty(t, problems)
	require.Contains(t, problems[0].Error(), "schedules")
}
