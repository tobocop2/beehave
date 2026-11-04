package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/model"
)

func TestProject_HostForChoosesADeclaredCluster(t *testing.T) {
	t.Parallel()

	project := model.Project{Name: "p", BuildHost: "gpu", BuildHosts: []string{"gpu", "gpu-b"}, Host: &model.HostConfig{}}

	got, err := project.HostFor("")
	require.NoError(t, err)
	assert.Equal(t, "gpu", got, "a run that names no host takes the build host of the project")

	got, err = project.HostFor("gpu-b")
	require.NoError(t, err)
	assert.Equal(t, "gpu-b", got)

	got, err = project.HostFor("gpu")
	require.NoError(t, err)
	assert.Equal(t, "gpu", got)

	_, err = project.HostFor("other")
	require.ErrorIs(t, err, model.ErrUndeclaredHost)
	require.ErrorContains(t, err, "gpu, gpu-b")

	single := model.Project{Name: "p", BuildHost: "gpu", Host: &model.HostConfig{}}

	got, err = single.HostFor("gpu")
	require.NoError(t, err)
	assert.Equal(t, "gpu", got, "a project with one cluster declares that cluster")

	_, err = single.HostFor("gpu-b")
	require.ErrorIs(t, err, model.ErrUndeclaredHost)

	_, err = model.Project{Name: "p", BuildHost: "local"}.HostFor("gpu")
	require.ErrorIs(t, err, model.ErrUndeclaredHost, "a project that builds here declares no cluster")

	got, err = model.Project{Name: "p", BuildHost: "local"}.HostFor("local")
	require.NoError(t, err, "a workflow that names its host local works on a project that builds here")
	assert.Equal(t, "local", got)
}

func TestClaims_NoForgeNameIsAClaimStore(t *testing.T) {
	t.Parallel()

	for _, forge := range model.Forges() {
		_, err := model.ParseClaims(string(forge))

		require.ErrorIs(t, err, model.ErrUnknownClaims, "claims = %q must be refused: the lock coordinates our own sessions and keeps its state off the forge", forge)
	}
}
