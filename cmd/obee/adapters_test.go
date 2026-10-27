package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/host/skypilot"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store/beads/beadstest"
)

func TestAdapters_TheTableHasAConstructorForEveryNameAndNothingElse(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t))

	for _, name := range model.Trackers() {
		assert.Contains(t, table.trackers, name)
	}

	for _, name := range model.Forges() {
		assert.Contains(t, table.forges, name)
	}

	assert.Len(t, table.trackers, len(model.Trackers()))
	assert.Len(t, table.forges, len(model.Forges()))
}

func TestAdapters_AgentBuildsTheClaudeAdapterAndNothingForAnAgentWithoutOne(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t))

	assert.Equal(t, claude.New(), table.agent(model.AgentClaude))
	assert.Nil(t, table.agent(model.AgentOpenCode))
}

func TestAdapters_BuildTheAdapterTheProjectNames(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t))
	project := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerBeads, Forge: model.ForgeGithub, BeadsPrefix: "p"}

	tracker, err := table.tracker(project)
	require.NoError(t, err)
	assert.NotNil(t, tracker)

	project.Tracker = model.TrackerGithub
	tracker, err = table.tracker(project)
	require.NoError(t, err)
	assert.NotNil(t, tracker)

	forge, err := table.forge(project)
	require.NoError(t, err)
	assert.NotNil(t, forge)
}

func TestAdapters_RefuseWhatTheyCannotBuild(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t))

	_, err := table.tracker(model.Project{Name: "p", Tracker: "jira"})
	require.EqualError(t, err, `project p: no tracker adapter named "jira"`)

	_, err = table.forge(model.Project{Name: "p", Forge: "gitlab"})
	require.EqualError(t, err, `project p: no forge adapter named "gitlab"`)

	tracker, err := table.tracker(model.Project{Name: "p", Tracker: model.TrackerBeads})
	require.ErrorContains(t, err, "the beads id prefix is required")
	assert.Nil(t, tracker)

	tracker, err = table.tracker(model.Project{Name: "p", Tracker: model.TrackerGithub, Repo: "bad"})
	require.Error(t, err)
	assert.Nil(t, tracker)

	forge, err := table.forge(model.Project{Name: "p", Forge: model.ForgeGithub, Repo: "bad"})
	require.Error(t, err)
	assert.Nil(t, forge)
}

func TestAdapters_CheckBuildsTheTrackerTheForgeAndTheLeaseStoreOfAProject(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t))
	good := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerGithub, Forge: model.ForgeGithub, BeadsPrefix: "p"}
	require.NoError(t, table.check(t.Context(), good))

	badTracker := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerBeads, Forge: model.ForgeGithub}
	require.ErrorContains(t, table.check(t.Context(), badTracker), "the beads id prefix is required")

	badForge := model.Project{Name: "p", Repo: "bad", Tracker: model.TrackerBeads, Forge: model.ForgeGithub, BeadsPrefix: "p"}
	require.Error(t, table.check(t.Context(), badForge))

	noPrefix := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerGithub, Forge: model.ForgeGithub}
	require.ErrorContains(t, table.check(t.Context(), noPrefix), "the beads id prefix is required")
}

func TestAdapters_CheckRefusesABdThatIsTooOld(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Version = "0.56.1"
	project := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerGithub, Forge: model.ForgeGithub, BeadsPrefix: "p"}

	require.EqualError(t, newAdapters(bd).check(t.Context(), project), "obee needs bd 1.3.2 or newer for claims; found 0.56.1")

	bd.Version = "1.3.2"
	require.NoError(t, newAdapters(bd).check(t.Context(), project))
}

func TestAdapters_TheHostTableHasAConstructorForEveryProviderAndNothingElse(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t))

	for _, name := range model.HostProviders() {
		assert.Contains(t, table.hosts, name)
	}

	assert.Len(t, table.hosts, len(model.HostProviders()))
}

func hostProject(provider model.HostProvider, buildHost string) model.Project {
	return model.Project{
		Name: "p", Repo: "o/n", Tracker: model.TrackerGithub, Forge: model.ForgeGithub, BeadsPrefix: "p", BuildHost: buildHost,
		Host: &model.HostConfig{Provider: provider, IdleMinutes: 60, MaxRun: time.Hour, DiskFloorGB: 50},
	}
}

func TestAdapters_HostBuildsTheProviderTheProjectNames(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t))

	built, err := table.host(hostProject(model.HostSkyPilot, "lane-host"))
	require.NoError(t, err)
	assert.IsType(t, &skypilot.Host{}, built)

	_, err = table.host(hostProject("ssh", "lane-host"))
	require.EqualError(t, err, `project p: no host adapter named "ssh"`)

	_, err = table.host(model.Project{Name: "p"})
	require.EqualError(t, err, "project p: no [host] table, so no build host")

	built, err = table.host(hostProject(model.HostSkyPilot, "-oProxyCommand=x"))
	require.ErrorIs(t, err, skypilot.ErrConfig)
	assert.Nil(t, built)
}

func TestAdapters_CheckRefusesAHostProviderItDoesNotKnowAndAHostItCannotBuild(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t))

	require.NoError(t, table.check(t.Context(), hostProject(model.HostSkyPilot, "lane-host")))
	require.ErrorContains(t, table.check(t.Context(), hostProject("ssh", "lane-host")), `no host adapter named "ssh"`)
	require.ErrorIs(t, table.check(t.Context(), hostProject(model.HostSkyPilot, "")), skypilot.ErrConfig)
}
