package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestProject_HasWriterOnlyWhenTheWriterRoleIsDeclared(t *testing.T) {
	t.Parallel()

	assert.True(t, model.Project{Credentials: map[model.Role]model.Credential{model.RoleWriter: {}}}.HasWriter())
	assert.False(t, model.Project{Credentials: map[model.Role]model.Credential{"other": {}}}.HasWriter())
	assert.False(t, model.Project{}.HasWriter())
}
