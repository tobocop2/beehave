package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/model"
)

func slotProject(name string, hosts []string, slots map[string]int) config.Project {
	project := model.Project{Name: name, BuildHost: hosts[0], Host: &model.HostConfig{Slots: slots}}
	if len(hosts) > 1 {
		project.BuildHosts = hosts
	}

	return config.Project{Project: project}
}

func TestSlotsOf_TheClassOfTheGatesGetsTheGateSlotsOfTheHostBlockWithNoEntryInSlots(t *testing.T) {
	t.Parallel()

	one := slotProject("one", []string{"gpu"}, nil)
	one.Host.GateSlots = 3
	two := slotProject("two", []string{"gpu"}, map[string]int{"mutation": 1})
	two.Host.GateSlots = 3

	grant := slotsOf(map[string]config.Project{"one": one, "two": two})

	got, err := grant("one", "gpu", model.GateClass)
	require.NoError(t, err)
	require.Equal(t, flow.SlotGrant{Host: "gpu", Count: 3}, got)

	got, err = grant("two", "", model.GateClass)
	require.NoError(t, err)
	require.Equal(t, flow.SlotGrant{Host: "gpu", Count: 3}, got)

	_, err = grant("two", "", "fuzz")
	require.ErrorIs(t, err, errNoSlotClass, "only the class of the gates needs no entry")
}

func TestSlotsOf_AClassTheHostBlockListsGetsItsCountAndAClassItLeavesOutIsRefusedWithTheClassAndTheHost(t *testing.T) {
	t.Parallel()

	grant := slotsOf(map[string]config.Project{
		"p":     slotProject("p", []string{"gpu", "cpu"}, map[string]int{"mutation": 1, "bench": 2}),
		"other": slotProject("other", []string{"gpu"}, map[string]int{"mutation": 1}),
		"local": {Project: model.Project{Name: "local", BuildHost: config.LocalHost}},
	})

	got, err := grant("p", "", "bench")
	require.NoError(t, err)
	require.Equal(t, flow.SlotGrant{Host: "gpu", Count: 2}, got, "an empty cluster is the default host of the project")

	got, err = grant("p", "gpu", "mutation")
	require.NoError(t, err)
	require.Equal(t, flow.SlotGrant{Host: "gpu", Count: 1}, got, "projects that share a host agree on the count")

	got, err = grant("p", "cpu", "mutation")
	require.NoError(t, err)
	require.Equal(t, flow.SlotGrant{Host: "cpu", Count: 1}, got)

	_, err = grant("p", "", "fuzz")
	require.ErrorIs(t, err, errNoSlotClass)
	require.ErrorContains(t, err, `"fuzz"`)
	require.ErrorContains(t, err, "host gpu")

	_, err = grant("other", "", "bench")
	require.ErrorIs(t, err, errNoSlotClass, "another project that names the class on the same host does not give it to this project")

	_, err = grant("p", "tpu", "bench")
	require.ErrorIs(t, err, model.ErrUndeclaredHost)

	_, err = grant("local", "", "bench")
	require.ErrorIs(t, err, errNoHost)

	_, err = grant("zzz", "", "bench")
	require.ErrorIs(t, err, errUnknownProject)
}
