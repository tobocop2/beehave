package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/model"
)

func TestConfig_AHostBlockReadsItsSlotClasses(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(`, slots = {"mutation": 1, "bench": 2}`)))

	require.Empty(t, problems)
	require.Equal(t, map[string]int{"mutation": 1, "bench": 2}, project.Host.Slots)

	project, problems = loadProjectText(t, call(clusterProject+", host = "+hostCall("")))

	require.Empty(t, problems)
	require.Nil(t, project.Host.Slots, "a host block with no slots names no class")
}

func TestConfig_AHostBlockTakesTheLargestSlotCountAndRefusesOneMore(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(`, slots = {"bench": 1048576}`)))

	require.Empty(t, problems)
	require.Equal(t, map[string]int{"bench": 1048576}, project.Host.Slots)

	_, problems = loadProjectText(t, call(clusterProject+", host = "+hostCall(`, slots = {"bench": 1048577}`)))

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "`host.slots` class \"bench\" must be above zero and at most 1048576; got 1048577")
}

func slotProject(name, host string, hosts []string, slots map[string]int) config.Project {
	return config.Project{Project: model.Project{Name: name, BuildHost: host, BuildHosts: hosts, Host: &model.HostConfig{Slots: slots}}}
}

func TestConfigValidate_TwoProjectsOnOneHostWithDifferentCountsForAClassAreRefused(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Projects = map[string]config.Project{
		"one": slotProject("one", "gpu", nil, map[string]int{"mutation": 1}),
		"two": slotProject("two", "cpu", []string{"cpu", "gpu"}, map[string]int{"mutation": 4}),
	}

	problems := cfg.Validate()

	require.Len(t, problems, 1)
	require.ErrorIs(t, problems[0], config.ErrRefused)
	require.ErrorContains(t, problems[0], `projects one and two both use host gpu and give a different count for the slot class "mutation": 1 and 4`)
}

func TestConfigValidate_ProjectsOnOneHostMayGiveEqualCountsOrDifferentClasses(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Projects = map[string]config.Project{
		"one":   slotProject("one", "gpu", nil, map[string]int{"mutation": 2, "bench": 1}),
		"two":   slotProject("two", "gpu", nil, map[string]int{"mutation": 2, "fuzz": 5}),
		"three": slotProject("three", "cpu", nil, map[string]int{"mutation": 9}),
		"local": {Project: model.Project{Name: "local", BuildHost: config.LocalHost}},
	}

	require.Empty(t, cfg.Validate(), "equal counts agree, a class that one project names has no rival, and another host has its own count")
}

func TestSlotCount_AHostThatProjectsShareRunsTheCountTheyAgreeOn(t *testing.T) {
	t.Parallel()

	projects := map[string]config.Project{
		"one":   slotProject("one", "gpu", nil, map[string]int{"mutation": 3}),
		"two":   slotProject("two", "cpu", []string{"cpu", "gpu"}, map[string]int{"mutation": 3, "bench": 2}),
		"local": {Project: model.Project{Name: "local", BuildHost: config.LocalHost}},
	}

	count, named := config.SlotCount(projects, "gpu", "mutation")
	require.True(t, named)
	require.Equal(t, 3, count)

	count, named = config.SlotCount(projects, "cpu", "bench")
	require.True(t, named)
	require.Equal(t, 2, count)

	_, named = config.SlotCount(projects, "gpu", "fuzz")
	require.False(t, named, "no project names the class")

	_, named = config.SlotCount(projects, "tpu", "mutation")
	require.False(t, named, "no project declares the host")
}

func TestConfig_AHostBlockRefusesASlotCountThatIsNotAWholeNumberAboveZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		slots string
		words string
	}{
		{"zero", `{"mutation": 0}`, "`host.slots` class \"mutation\" must be above zero and at most 1048576; got 0"},
		{"negative", `{"bench": 2, "mutation": -1}`, "`host.slots` class \"mutation\" must be above zero and at most 1048576; got -1"},
		{"huge", `{"bench": 17179869184}`, "`host.slots` class \"bench\" must be above zero and at most 1048576; got 17179869184"},
		{"beyond 64 bits", `{"bench": 1180591620717411303424}`, "`host.slots` class \"bench\" must be above zero and at most 1048576"},
		{"text", `{"mutation": "1"}`, "`host.slots` class \"mutation\" holds a string, want a whole number above zero"},
		{"fraction", `{"mutation": 1.5}`, "`host.slots` class \"mutation\" holds a float, want a whole number above zero"},
		{"blank class", `{" ": 1}`, "`host.slots` has the class \" \". Use letters, digits"},
		{"class with a colon", `{"a:b": 1}`, "`host.slots` has the class \"a:b\""},
		{"not a dict", `["mutation"]`, "`host.slots`: got list, want a dict of class names to counts"},
		{"class that is not a name", `{1: 1}`, "`host.slots` has the key 1, want a class name"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(", slots = "+tc.slots)))

			require.Len(t, problems, 1)
			require.ErrorIs(t, problems[0], config.ErrRefused)
			require.ErrorContains(t, problems[0], tc.words)
		})
	}
}
