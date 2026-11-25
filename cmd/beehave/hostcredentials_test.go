package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/model"
)

// checkedHost is a build host with a credential check that records the cluster it checks, and refuses the cluster named refused.
type checkedHost struct {
	host.Host

	cluster, refused string
	checked          *[]string
}

func (c checkedHost) CheckCredentials(context.Context) error {
	*c.checked = append(*c.checked, c.cluster)
	if c.cluster == c.refused {
		return errors.New("the build host " + c.cluster + " gets the SSH agent")
	}

	return nil
}

// plainHost is a build host whose provider has no credential check.
type plainHost struct{ host.Host }

// projectOnTwoClusters returns a project that declares two clusters on one host block with the credentials value given.
func projectOnTwoClusters(credentials model.HostCredentials) model.Project {
	project := localProjectNamed("p")
	project.BuildHost, project.BuildHosts = "gpu", []string{"gpu", "gpu-b"}
	project.Host = &model.HostConfig{Provider: skypilot.Name, Credentials: credentials}

	return project
}

// tableOfCheckedHosts returns adapters whose one host provider builds a checkedHost for the cluster of the project.
func tableOfCheckedHosts(refused string, checked *[]string) adapters {
	table := newAdapters(nil, nil, nil)
	table.hosts = map[model.HostProvider]hostAdapter{
		skypilot.Name: {build: func(project model.Project) (host.Host, error) {
			return checkedHost{cluster: project.BuildHost, refused: refused, checked: checked}, nil
		}},
	}

	return table
}

func TestAdapters_CheckRunsTheCredentialCheckOfEveryDeclaredClusterOfAHostDeclaredNone(t *testing.T) {
	t.Parallel()

	var checked []string

	require.NoError(t, tableOfCheckedHosts("", &checked).check(t.Context(), projectOnTwoClusters(model.HostCredentialsNone)))
	assert.Equal(t, []string{"gpu", "gpu-b"}, checked)

	checked = nil
	err := tableOfCheckedHosts("gpu-b", &checked).check(t.Context(), projectOnTwoClusters(model.HostCredentialsNone))
	require.ErrorContains(t, err, "the build host gpu-b gets the SSH agent", "a cluster that is not the default is checked too")
}

func TestAdapters_CheckRunsNoCredentialCheckForAHostWithoutTheDeclaration(t *testing.T) {
	t.Parallel()

	var checked []string

	require.NoError(t, tableOfCheckedHosts("gpu", &checked).check(t.Context(), projectOnTwoClusters("")))
	assert.Empty(t, checked)
}

func TestAdapters_CheckRefusesAHostDeclaredNoneWhoseProviderHasNoCredentialCheck(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, nil, nil)
	table.hosts = map[model.HostProvider]hostAdapter{
		skypilot.Name: {build: func(model.Project) (host.Host, error) { return plainHost{}, nil }},
	}

	err := table.check(t.Context(), projectOnTwoClusters(model.HostCredentialsNone))
	require.ErrorContains(t, err, `cannot check credentials = "none"`)

	require.NoError(t, table.check(t.Context(), projectOnTwoClusters("")), "a host without the declaration needs no check")
}

func TestAdapters_EachHostProviderOfTheTableHasACredentialCheck(t *testing.T) {
	t.Parallel()

	for name, entry := range hostEntries(nil) {
		project := projectOnTwoClusters(model.HostCredentialsNone)
		project.Host.Provider, project.Host.IdleMinutes, project.Host.MaxRun = name, 60, time.Hour

		built, err := entry.build(project)
		require.NoError(t, err, name)

		_, ok := built.(credentialCheck)
		assert.True(t, ok, name)
	}
}
