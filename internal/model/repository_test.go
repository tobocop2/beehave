package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestProject_HasRepositoryIsTrueForASourceOrAForgeOnly(t *testing.T) {
	t.Parallel()

	assert.False(t, model.Project{}.HasRepository())
	assert.True(t, model.Project{Source: "/srv/repo"}.HasRepository())
	assert.True(t, model.Project{Forge: "github"}.HasRepository())
}
