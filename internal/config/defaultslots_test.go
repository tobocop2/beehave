package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

func TestConfigValidate_TwoProjectsOnOneHostMustAgreeOnTheCountOfTheDefaultClass(t *testing.T) {
	t.Parallel()

	cfg := config.DefaultConfig()
	cfg.Projects = map[string]config.Project{
		"one": slotProject("one", "gpu", nil, map[string]int{model.DefaultClass: 3}),
		"two": slotProject("two", "gpu", nil, nil),
	}

	problems := cfg.Validate()

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], `give a different count for the slot class "default": 3 and 1`)
}

func TestConfig_ADefaultClassWithZeroSlotsIsRefusedAtLoad(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(`, slots = {"default": 0}`)))

	require.Len(t, problems, 1)
	require.ErrorContains(t, problems[0], "`host.slots` class \"default\" must be above zero")
}

func TestSlotCount_TheDefaultClassHasOneSlotWhenTheHostBlockDoesNotNameIt(t *testing.T) {
	t.Parallel()

	projects := map[string]config.Project{"one": slotProject("one", "gpu", nil, nil)}

	count, named := config.SlotCount(projects, "gpu", model.DefaultClass)

	require.True(t, named)
	require.Equal(t, model.DefaultSlots, count)
}
