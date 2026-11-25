package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestParseAssociation_ReadsTheEightValuesOfTheForgeAndRefusesAnyOther(t *testing.T) {
	t.Parallel()

	for _, text := range []string{"OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR", "FIRST_TIME_CONTRIBUTOR", "FIRST_TIMER", "MANNEQUIN", "NONE"} {
		got, err := model.ParseAssociation(text)
		require.NoError(t, err, text)
		assert.Equal(t, model.Association(text), got)
	}

	for _, text := range []string{"", "owner", "OWNER ", "ADMIN"} {
		_, err := model.ParseAssociation(text)
		require.ErrorIs(t, err, model.ErrUnknownAssociation, text)
		assert.ErrorContains(t, err, `"`+text+`"`)
	}
}

func TestAuthored_OnlyTheOwnerOrNothingIsNotForeign(t *testing.T) {
	t.Parallel()

	assert.False(t, model.Authored{}.Foreign(), "no issue or pull request")
	assert.False(t, model.Authored{Number: 7, Association: model.AssociationOwner}.Foreign())

	for _, association := range []model.Association{model.AssociationMember, model.AssociationCollaborator, model.AssociationNone, ""} {
		assert.True(t, model.Authored{Number: 7, Association: association}.Foreign(), association)
	}
}

func TestPullFacts_AuthoredIsThePullRequestOfTheFactsWithItsAuthor(t *testing.T) {
	t.Parallel()

	facts := model.PullFacts{Number: 31, State: "OPEN", Author: "dev", Association: model.AssociationContributor}

	assert.Equal(t, model.Authored{Number: 31, Association: model.AssociationContributor}, facts.Authored())
	assert.Zero(t, model.PullFacts{}.Authored(), "facts with no pull request have none")
}
