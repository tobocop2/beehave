package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/model"
)

func TestGateSlots_ADefaultHostBlockHasOneGateSlot(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall("")))

	require.Empty(t, problems)
	assert.Equal(t, 1, project.Host.GateSlots)
}

func TestGateSlots_ReadsItsCount(t *testing.T) {
	t.Parallel()

	project, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(", gate_slots = 4")))

	require.Empty(t, problems)
	assert.Equal(t, 4, project.Host.GateSlots)

	project, problems = loadProjectText(t, call(clusterProject+", host = "+hostCall(", gate_slots = 1048576")))

	require.Empty(t, problems)
	assert.Equal(t, 1048576, project.Host.GateSlots)
}

func TestGateSlots_RefusesABadValueAndNamesTheKey(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"0", "-1", "1048577"} {
		_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(", gate_slots = "+value)))

		require.Len(t, problems, 1, value)
		assert.Contains(t, problems[0].Error(), "`host.gate_slots` must be above zero and at most 1048576", value)
	}

	for _, value := range []string{`"2"`, "2.5"} {
		_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(", gate_slots = "+value)))

		require.Len(t, problems, 1, value)
		assert.Contains(t, problems[0].Error(), "gate_slots", value)
		assert.NotContains(t, problems[0].Error(), "unexpected", value, "the key is known and its value is wrong")
	}
}

func TestGateSlots_TwoProjectsOnOneHostWithDifferentCountsAreRefusedAndEqualCountsAreNot(t *testing.T) {
	t.Parallel()

	build := func(first, second int) config.Problems {
		one := slotProject("one", "gpu", nil, nil)
		two := slotProject("two", "gpu", nil, nil)
		one.Host.GateSlots, two.Host.GateSlots = first, second

		cfg := config.DefaultConfig()
		cfg.Projects = map[string]config.Project{"one": one, "two": two}

		return cfg.Validate()
	}

	problems := build(2, 4)

	require.NotEmpty(t, problems)
	assert.Contains(t, problems.Error(), `"gate"`, "the refusal names the reserved class and both counts")
	assert.Contains(t, problems.Error(), "2")
	assert.Contains(t, problems.Error(), "4")

	assert.Empty(t, build(3, 3), "equal counts are fine")
	assert.Empty(t, build(0, 0), "a project built by hand with no gate slots claims none")
}

func TestGateSlots_TheCountOfAHostIsFoundUnderTheReservedClass(t *testing.T) {
	t.Parallel()

	one := slotProject("one", "gpu", nil, nil)
	one.Host.GateSlots = 3

	count, found := config.SlotCount(map[string]config.Project{"one": one}, "gpu", model.GateClass)

	assert.True(t, found)
	assert.Equal(t, 3, count)

	_, found = config.SlotCount(map[string]config.Project{"one": slotProject("two", "gpu", nil, nil)}, "gpu", model.GateClass)

	assert.False(t, found, "a host block with no gate slots gives no count")
}

func TestGateSlots_TheClassGateIsReservedInSlots(t *testing.T) {
	t.Parallel()

	_, problems := loadProjectText(t, call(clusterProject+", host = "+hostCall(`, slots = {"gate": 2}`)))

	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "gate_slots")
}
