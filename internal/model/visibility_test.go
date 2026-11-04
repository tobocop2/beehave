package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/model"
)

func TestParseVisibility(t *testing.T) {
	t.Parallel()

	for _, name := range model.Visibilities() {
		got, err := model.ParseVisibility(string(name))
		require.NoError(t, err)
		assert.Equal(t, name, got)
	}

	_, err := model.ParseVisibility("internal")
	require.ErrorIs(t, err, model.ErrUnknownVisibility)
	assert.EqualError(t, err, `unknown visibility "internal": use one of public, private`)
}

func TestProject_OnlyAnExplicitPrivateVisibilityIsPrivate(t *testing.T) {
	t.Parallel()

	assert.True(t, model.Project{Visibility: model.VisibilityPrivate}.IsPrivate())
	assert.False(t, model.Project{Visibility: model.VisibilityPublic}.IsPrivate())
	assert.False(t, model.Project{}.IsPrivate())
}
