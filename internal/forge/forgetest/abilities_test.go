package forgetest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/forge/forgetest"
	"github.com/tobocop2/beehave/internal/model"
)

// alerting serves the alert read and the ready-mark read. down fails the alert read.
type alerting struct {
	memOptional
}

func (a alerting) OpenAlerts(context.Context, int, string) ([]model.Alert, error) {
	if a.down {
		return nil, errDown
	}

	return nil, nil
}

func TestCheckAbilities_AcceptsTheDeclaredSetAndFailsAWrongOne(t *testing.T) {
	t.Parallel()

	target := alerting{}

	r := &recorder{}
	forgetest.CheckAbilities(r, target, []forge.Ability{forge.AbilityAlerts, forge.AbilityAssignees, forge.AbilityReadyMarks})
	assert.Empty(t, r.failures, "the declared set is the set that the forge has")

	r = &recorder{}
	forgetest.CheckAbilities(r, target, []forge.Ability{forge.AbilityAssignees, forge.AbilityReadyMarks})
	require.Len(t, r.failures, 1, "an ability that the forge has and the set lacks")
	assert.Contains(t, r.failures[0], "security alerts")

	r = &recorder{}
	forgetest.CheckAbilities(r, target, []forge.Ability{forge.AbilityAlerts, forge.AbilityAssignees, forge.AbilityReadyMarks, forge.AbilityBoards})
	require.Len(t, r.failures, 1, "an ability that the set lists and the forge lacks")
	assert.Contains(t, r.failures[0], "project boards")
}

func TestCheckAbilities_ABareForgeHasNoOptionalAbility(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	forgetest.CheckAbilities(r, memForge{}, nil)
	assert.Empty(t, r.failures, "the fake that lacks every optional ability has the empty set")

	r = &recorder{}
	forgetest.CheckAbilities(r, memForge{}, []forge.Ability{forge.AbilityStatuses})
	assert.Len(t, r.failures, 1, "a set that lists an ability that the bare forge lacks fails")
}

func TestTestForgeAbilities_PassesTheDeclaredSetOfAForge(t *testing.T) {
	t.Parallel()

	forgetest.TestForgeAbilities(t, memForge{}, nil)
}

func TestCheckAlertsFail_AcceptsAForgeThatErrorsAndFailsOneThatReadsAnEmptyList(t *testing.T) {
	t.Parallel()

	r := &recorder{}
	forgetest.CheckAlertsFail(r, alerting{memOptional{down: true}})
	assert.Empty(t, r.failures)

	r = &recorder{}
	forgetest.CheckAlertsFail(r, alerting{})
	assert.Equal(t, []string{"OpenAlerts: want an error from a failed command, got none"}, r.failures)
}
