package forge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/forge"
)

// holders gives one fake for each ability. Each fake implements the interface of its ability and no other.
func holders() map[forge.Ability]any {
	return map[forge.Ability]any{
		forge.AbilityStatuses:    struct{ forge.Statuses }{},
		forge.AbilityOpener:      struct{ forge.Opener }{},
		forge.AbilityStacks:      struct{ forge.Stacker }{},
		forge.AbilitySyncer:      struct{ forge.Syncer }{},
		forge.AbilityAssignees:   struct{ forge.AssigneeReads }{},
		forge.AbilityAssociation: struct{ forge.AssociationReads }{},
		forge.AbilityReadyMarks:  struct{ forge.ReadyReads }{},
		forge.AbilityBoards:      struct{ forge.Boarder }{},
		forge.AbilityQueue:       struct{ forge.Queuer }{},
		forge.AbilityAncestry:    struct{ forge.Ancestry }{},
		forge.AbilityTrees:       struct{ forge.Trees }{},
		forge.AbilityLogs:        struct{ forge.Logs }{},
		forge.AbilityAlerts:      struct{ forge.AlertReads }{},
	}
}

func TestNoCapability_MatchesErrNoCapabilityAndNamesTheAbility(t *testing.T) {
	t.Parallel()

	err := forge.NoCapability(forge.AbilityStatuses, "the instance has no statuses")

	require.ErrorIs(t, err, forge.ErrNoCapability)
	assert.ErrorContains(t, err, "commit statuses")
	assert.ErrorContains(t, err, "the instance has no statuses")
	assert.NotErrorIs(t, errors.New("other"), forge.ErrNoCapability)
}

func TestHas_ReportsEachAbilityByItsInterface(t *testing.T) {
	t.Parallel()

	all := holders()
	require.Len(t, all, len(forge.Abilities()), "every ability has a fake")

	for _, ability := range forge.Abilities() {
		for other, fake := range all {
			assert.Equal(t, ability == other, forge.Has(fake, ability), "the fake of %q asked for %q", other, ability)
		}

		assert.False(t, forge.Has(struct{}{}, ability), "a forge with no optional interface has no %q", ability)
	}
}

func TestNeed_AcceptsAForgeThatHasEveryAbilityAsked(t *testing.T) {
	t.Parallel()

	require.NoError(t, forge.Need(t.Context(), holders()[forge.AbilityAlerts], forge.AbilityAlerts))
	require.NoError(t, forge.Need(t.Context(), struct{}{}))
}

func TestNeed_NamesEveryMissingAbility(t *testing.T) {
	t.Parallel()

	err := forge.Need(t.Context(), holders()[forge.AbilityLogs], forge.AbilityStatuses, forge.AbilityLogs, forge.AbilityAlerts)

	require.ErrorIs(t, err, forge.ErrNoCapability)
	assert.ErrorContains(t, err, "commit statuses")
	assert.ErrorContains(t, err, "security alerts")
	assert.NotContains(t, err.Error(), "check logs")
}

// denying is a forge with the alert read and a Probe that denies it.
type denying struct {
	forge.AlertReads
	err error
}

func (d denying) Probe(_ context.Context, a forge.Ability) error {
	if a == forge.AbilityAlerts {
		return d.err
	}

	return nil
}

func TestNeed_RefusesAnAbilityThatTheProbeDenies(t *testing.T) {
	t.Parallel()

	err := forge.Need(t.Context(), denying{err: forge.NoCapability(forge.AbilityAlerts, "code scanning is off")}, forge.AbilityAlerts)

	require.ErrorIs(t, err, forge.ErrNoCapability)
	assert.ErrorContains(t, err, "code scanning is off")
}

func TestNeed_ReturnsAFailedProbeAsAnErrorThatIsNoMissingAbility(t *testing.T) {
	t.Parallel()

	down := errors.New("the read failed")
	err := forge.Need(t.Context(), denying{err: down}, forge.AbilityAlerts)

	require.ErrorIs(t, err, down)
	assert.NotErrorIs(t, err, forge.ErrNoCapability)
}

func TestNeed_AcceptsAnAbilityThatTheProbeAccepts(t *testing.T) {
	t.Parallel()

	require.NoError(t, forge.Need(t.Context(), denying{}, forge.AbilityAlerts))
}
