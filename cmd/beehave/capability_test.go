package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/flow"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/merge"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/roundsync"
)

// The merge owns its own copy of each optional interface, because it may not import the forge. A forge ability must give the merge everything that it reads.
var (
	_ merge.Ancestry   = forge.Ancestry(nil)
	_ merge.Queuer     = forge.Queuer(nil)
	_ merge.Trees      = forge.Trees(nil)
	_ merge.Logs       = forge.Logs(nil)
	_ merge.AlertReads = forge.AlertReads(nil)
)

func TestOptionalStep_OnAForgeThatLacksTheAbilityMatchesErrNoCapability(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	bare := opened{plainForge{}}

	_, assignees := bare.IssueAssignees(ctx, "", 1)
	_, marks := bare.ReadyMarks(ctx, 1)
	_, association := bare.Association(ctx, 1)
	_, opening := openOver(plainForge{}).Open(ctx, "p", "bb-5", flow.OpenRequest{Title: "t", Base: "main"})
	_, boards := roundWrites{pulls: roundPulls(plainForge{}, "")}.Place(ctx, "p", model.Card{})

	for name, err := range map[string]error{"assignees": assignees, "ready marks": marks, "association": association, "opener": opening, "boards": boards} {
		require.ErrorIs(t, err, forge.ErrNoCapability, name)
	}

	require.ErrorContains(t, assignees, string(forge.AbilityAssignees))
	require.ErrorContains(t, boards, string(forge.AbilityBoards))
}

// partial has the abilities of a sync and lacks a call that the sync makes but no ability names.
type partial struct {
	plainForge
	forge.Syncer
	forge.Opener
}

func TestSideOf_AForgeWithTheAbilitiesAndNotTheCallNamesTheFirstAbility(t *testing.T) {
	t.Parallel()

	_, err := sideOf[roundsync.Forge](t.Context(), partial{}, forge.AbilitySyncer, forge.AbilityOpener)

	require.ErrorIs(t, err, forge.ErrNoCapability)
	require.ErrorContains(t, err, string(forge.AbilitySyncer))
}
