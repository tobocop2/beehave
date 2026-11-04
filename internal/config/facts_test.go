package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const factsCall = `, people = {"owner": "ann", "third_party_idle_hours": "24"}, epics = {"bugs": "E-1"}, gate_files = [".github/workflows/ci.yml"], ` +
	`conventions = ["AGENTS.md"], brief_rules = ["Name the tempting wrong fix"], stacked_rules = ["Merge the base first"], ` +
	`limits = ["Never run rm on a glob"], traps = ["The cache hides a stale build"], token_rules = ["Read a log by its summary lines"]`

func TestLoadFolder_AProjectCarriesTheFactsOfItsRepository(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject+factsCall))

	require.Empty(t, problems)
	require.Equal(t, map[string]string{"owner": "ann", "third_party_idle_hours": "24"}, project.People)
	require.Equal(t, map[string]string{"bugs": "E-1"}, project.Epics)
	require.Equal(t, []string{".github/workflows/ci.yml"}, project.GateFiles)
	require.Equal(t, []string{"AGENTS.md"}, project.Conventions)
	require.Equal(t, []string{"Name the tempting wrong fix"}, project.BriefRules)
	require.Equal(t, []string{"Merge the base first"}, project.StackedRules)
	require.Equal(t, []string{"Never run rm on a glob"}, project.Limits)
	require.Equal(t, []string{"The cache hides a stale build"}, project.Traps)
	require.Equal(t, []string{"Read a log by its summary lines"}, project.TokenRules)
}

func TestLoadFolder_TheFactsAreEmptyWhenTheFileLeavesThemOut(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(localProject))

	require.Empty(t, problems)
	require.Empty(t, project.People)
	require.Empty(t, project.Epics)
	require.Empty(t, project.Limits)
	require.Empty(t, project.Traps)
}

func TestLoadFolder_ADictEntryOfTheWrongTypeNamesItsKey(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, "\n"+call(localProject+`, people = {"owner": 1}`))

	require.Len(t, problems, 1)
	require.Contains(t, problems[0].Error(), `the entry "owner" holds a string and a int`)
}

func TestLoadFolder_ABadFactIsRefusedWithTheFileAndTheLine(t *testing.T) {
	t.Parallel()

	for name, args := range map[string]string{
		"a list of numbers":    `limits = [1]`,
		"a dict for a list":    `traps = {"a": "b"}`,
		"a list for a dict":    `people = ["ann"]`,
		"a number as a role":   `people = {"owner": 1}`,
		"a blank role":         `people = {" ": "ann"}`,
		"a blank person":       `people = {"owner": ""}`,
		"a blank rule":         `brief_rules = ["  "]`,
		"a blank convention":   `conventions = [""]`,
		"a blank queue":        `epics = {"": "E-1"}`,
		"a blank stacked rule": `stacked_rules = [""]`,
	} {
		_, problems := loadProjectText(t, "\n"+call(localProject+", "+args))

		require.Len(t, problems, 1, name)
		require.Contains(t, problems[0].Error(), "p.star:2:", name)
	}
}
