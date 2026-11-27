package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehest/internal/config"
	"github.com/tobocop2/beehest/internal/flow"
	"github.com/tobocop2/beehest/internal/model"
)

func TestDepsOn_TheEngineResolvesTheSlotClassesOfTheProjectsOfTheConfig(t *testing.T) {
	t.Parallel()

	cfg := config.Config{Projects: map[string]config.Project{"plain": slotProject("plain", []string{"gpu"}, nil)}}

	deps := depsOn(cfg, flow.Options{StateDir: t.TempDir()}, logins{}, newAdapters(nil, nil, nil))

	require.NotNil(t, deps.Slots, "without the resolver a host job takes no slot and ignores the cap of the host")

	got, err := deps.Slots("plain", "", model.DefaultClass)
	require.NoError(t, err)
	require.Equal(t, flow.SlotGrant{Host: "gpu", Count: model.DefaultSlots}, got)
}

func TestSlotsOf_TheDefaultClassNeedsNoEntryAndTakesTheCountOfTheHostBlockWhenItNamesOne(t *testing.T) {
	t.Parallel()

	grant := slotsOf(map[string]config.Project{
		"plain": slotProject("plain", []string{"gpu"}, nil),
		"named": slotProject("named", []string{"cpu"}, map[string]int{model.DefaultClass: 4}),
	})

	got, err := grant("plain", "", model.DefaultClass)
	require.NoError(t, err)
	require.Equal(t, flow.SlotGrant{Host: "gpu", Count: model.DefaultSlots}, got)

	got, err = grant("named", "", model.DefaultClass)
	require.NoError(t, err)
	require.Equal(t, flow.SlotGrant{Host: "cpu", Count: 4}, got)
}
