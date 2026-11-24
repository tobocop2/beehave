package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/model"
)

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
