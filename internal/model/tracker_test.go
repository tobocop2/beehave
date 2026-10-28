package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestParseTracker(t *testing.T) {
	t.Parallel()

	for _, name := range model.Trackers() {
		got, err := model.ParseTracker(string(name))
		require.NoError(t, err)
		assert.Equal(t, name, got)
	}

	_, err := model.ParseTracker("jira")
	require.ErrorIs(t, err, model.ErrUnknownTracker)
	assert.EqualError(t, err, `unknown tracker "jira": use one of local, beads, github`)
}

func TestParseClaims(t *testing.T) {
	t.Parallel()

	for _, name := range model.ClaimsNames() {
		got, err := model.ParseClaims(string(name))
		require.NoError(t, err)
		assert.Equal(t, name, got)
	}

	_, err := model.ParseClaims("github")
	require.ErrorIs(t, err, model.ErrUnknownClaims)
	assert.EqualError(t, err, `unknown claims "github": use one of local, beads`)
}

func TestParseForge(t *testing.T) {
	t.Parallel()

	for _, name := range model.Forges() {
		got, err := model.ParseForge(string(name))
		require.NoError(t, err)
		assert.Equal(t, name, got)
	}

	_, err := model.ParseForge("gitlab")
	require.ErrorIs(t, err, model.ErrUnknownForge)
	assert.EqualError(t, err, `unknown forge "gitlab": use one of github`)
}
