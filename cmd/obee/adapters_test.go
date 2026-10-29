package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/agent/claude"
	"github.com/tobocop2/obee/internal/forge"
	"github.com/tobocop2/obee/internal/host/skypilot"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/store/beads/beadstest"
)

func TestAdapters_TheTableHasAConstructorForEveryNameAndNothingElse(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)

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

	table := newAdapters(beadstest.NewBd(t), nil)

	assert.Equal(t, claude.New(), table.agent(model.AgentClaude))
	assert.Nil(t, table.agent(model.AgentOpenCode))
}

func TestAdapters_BuildTheAdapterTheProjectNames(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)
	project := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerBeads, Forge: model.ForgeGithub, Claims: model.ClaimsBeads, BeadsPrefix: "p"}

	tracker, err := table.tracker(t.Context(), project)
	require.NoError(t, err)
	assert.NotNil(t, tracker)

	project.Tracker = model.TrackerGithub
	tracker, err = table.tracker(t.Context(), project)
	require.NoError(t, err)
	assert.NotNil(t, tracker)

	forge, err := table.forge(project)
	require.NoError(t, err)
	assert.NotNil(t, forge)
}

func TestAdapters_RefuseWhatTheyCannotBuild(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)

	_, err := table.tracker(t.Context(), model.Project{Name: "p", Tracker: "jira"})
	require.EqualError(t, err, `project p: no tracker adapter named "jira"`)

	_, err = table.forge(model.Project{Name: "p", Forge: "gitlab"})
	require.EqualError(t, err, `project p: no forge adapter named "gitlab"`)

	tracker, err := table.tracker(t.Context(), model.Project{Name: "p", Tracker: model.TrackerBeads})
	require.ErrorContains(t, err, "the beads id prefix is required")
	assert.Nil(t, tracker)

	tracker, err = table.tracker(t.Context(), model.Project{Name: "p", Tracker: model.TrackerGithub, Repo: "bad"})
	require.Error(t, err)
	assert.Nil(t, tracker)

	forge, err := table.forge(model.Project{Name: "p", Forge: model.ForgeGithub, Repo: "bad"})
	require.Error(t, err)
	assert.Nil(t, forge)
}

func TestAdapters_CheckTrackerAndForgeAcceptsAProjectWhoseAdaptersBuild(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)
	good := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerGithub, Forge: model.ForgeGithub, Claims: model.ClaimsBeads, BeadsPrefix: "p"}

	require.NoError(t, table.checkTrackerAndForge(t.Context(), good))
}

func TestAdapters_CheckTrackerAndForgeRefusesAProjectWhoseAdapterCannotBeBuilt(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)

	err := table.checkTrackerAndForge(t.Context(), model.Project{Name: "p", Tracker: "jira"})
	require.EqualError(t, err, `project p: no tracker adapter named "jira"`)

	err = table.checkTrackerAndForge(t.Context(), model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerGithub, Forge: "gitlab"})
	require.EqualError(t, err, `project p: no forge adapter named "gitlab"`)
}

func TestAdapters_CheckBuildsTheTrackerTheForgeAndTheLeaseStoreOfAProject(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)
	good := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerGithub, Forge: model.ForgeGithub, Claims: model.ClaimsBeads, BeadsPrefix: "p"}
	require.NoError(t, table.check(t.Context(), good))

	badTracker := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerBeads, Forge: model.ForgeGithub}
	require.ErrorContains(t, table.check(t.Context(), badTracker), "the beads id prefix is required")

	badForge := model.Project{Name: "p", Repo: "bad", Tracker: model.TrackerBeads, Forge: model.ForgeGithub, Claims: model.ClaimsBeads, BeadsPrefix: "p"}
	require.Error(t, table.check(t.Context(), badForge))

	noPrefix := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerGithub, Forge: model.ForgeGithub, Claims: model.ClaimsBeads}
	require.ErrorContains(t, table.check(t.Context(), noPrefix), "the beads id prefix is required")
}

func stateAt(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

func localProjectNamed(name string) model.Project {
	return model.Project{Name: name, Source: "/srv/repo", Tracker: model.TrackerLocal, Claims: model.ClaimsLocal}
}

func TestAdapters_TheLocalTrackerOpensLocalDbInTheStateFolderAndRefusesAnUnknownItem(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	table := newAdapters(nil, stateAt(state))

	tracker, err := table.tracker(t.Context(), localProjectNamed("p"))
	require.NoError(t, err)

	closer, ok := tracker.(io.Closer)
	require.True(t, ok, "a local tracker holds a file, so it closes")
	t.Cleanup(func() { require.NoError(t, closer.Close()) })

	info, err := os.Stat(filepath.Join(state, "local.db"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	_, err = tracker.Item(t.Context(), "fix-login")
	require.EqualError(t, err, "no item fix-login in project p. Add it: obee item add")
}

func TestAdapters_TheLocalClaimStoreHoldsAndFreesARowInTheStateFolder(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, stateAt(t.TempDir()))

	rows, err := table.leases(t.Context(), localProjectNamed("p"))
	require.NoError(t, err)

	row := model.ClaimRow{Item: "fix-login", Holder: "ownerone"}
	require.NoError(t, rows.TryHold(t.Context(), row))

	held, err := rows.HeldRows(t.Context())
	require.NoError(t, err)
	assert.Len(t, held, 1)
	require.NoError(t, rows.Close())
}

func TestAdapters_ItemOpenerOpensTheItemListOfALocalProjectAndReportsAStateFolderThatFails(t *testing.T) {
	t.Parallel()

	opened, err := newAdapters(nil, stateAt(t.TempDir())).itemOpener()(t.Context(), localProjectNamed("p"))
	require.NoError(t, err)
	require.NoError(t, opened.Add(t.Context(), "fix-login", "Fix login", "body"))
	require.NoError(t, opened.Close())

	opened, err = newAdapters(nil, func() (string, error) { return "", errFake }).itemOpener()(t.Context(), localProjectNamed("p"))
	require.ErrorIs(t, err, errFake)
	assert.Nil(t, opened)
}

func TestAdapters_ALocalStoreThatCannotOpenIsAnError(t *testing.T) {
	t.Parallel()

	failing := func() (string, error) { return "", errFake }
	table := newAdapters(nil, failing)

	tracker, err := table.tracker(t.Context(), localProjectNamed("p"))
	require.ErrorIs(t, err, errFake)
	assert.Nil(t, tracker)

	_, err = table.leases(t.Context(), localProjectNamed("p"))
	require.ErrorIs(t, err, errFake)

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	_, err = newAdapters(nil, stateAt(filepath.Join(file, "state"))).tracker(t.Context(), localProjectNamed("p"))
	require.Error(t, err)
}

func TestAdapters_ALocalProjectNeedsNoBdAndCheckBuildsNothingOnDisk(t *testing.T) {
	t.Parallel()

	state := filepath.Join(t.TempDir(), "state")
	table := newAdapters(nil, stateAt(state))
	project := localProjectNamed("p")

	require.NoError(t, table.check(t.Context(), project))

	_, err := os.Stat(state)
	require.ErrorIs(t, err, os.ErrNotExist, "check leaves the state folder alone")
}

func TestAdapters_CheckRefusesABeadsClaimStoreWithNoPrefixEvenWhenTheTrackerIsLocal(t *testing.T) {
	t.Parallel()

	project := model.Project{Name: "p", Source: "/srv/repo", Tracker: model.TrackerLocal, Claims: model.ClaimsBeads}

	require.ErrorContains(t, newAdapters(beadstest.NewBd(t), nil).check(t.Context(), project), "the beads id prefix is required")

	project.BeadsPrefix = "p"
	bd := beadstest.NewBd(t)
	bd.Version = "0.56.1"
	require.ErrorContains(t, newAdapters(bd, nil).check(t.Context(), project), "found 0.56.1")
}

func TestAdapters_AProjectWithNoForgeHasNoForgeToBuildAndNoForgeFields(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)
	noForge := model.Project{Name: "p", Source: "/srv/repo", Tracker: model.TrackerBeads, Claims: model.ClaimsBeads, BeadsPrefix: "p"}

	built, err := table.forge(noForge)
	require.EqualError(t, err, `project p: no forge; set forge = "github" in the project file`)
	require.ErrorIs(t, err, forge.ErrNoForge)
	assert.Nil(t, built)

	require.NoError(t, table.check(t.Context(), noForge))
}

func TestAdapters_ABeadsClaimStoreWithNoPrefixAndAnUnknownClaimsNameAreRefusedByLeasesAndCheck(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)

	rows, err := table.leases(t.Context(), model.Project{Name: "p", Claims: model.ClaimsBeads})
	require.ErrorContains(t, err, "the beads id prefix is required")
	assert.Nil(t, rows.Rows)

	require.EqualError(t, table.check(t.Context(), model.Project{Name: "p", Source: "/srv/repo", Tracker: model.TrackerLocal, Claims: "zk"}), `project p: no claim store named "zk"`)
}

func TestStateFolder_AnswersWithTheFolderOfTheOptions(t *testing.T) {
	t.Parallel()

	dir, err := stateFolder("/var/obee")()

	require.NoError(t, err)
	assert.Equal(t, "/var/obee", dir)
}

func TestAdapters_AClaimsNameThatIsNeitherLocalNorBeadsIsRefused(t *testing.T) {
	t.Parallel()

	_, err := newAdapters(beadstest.NewBd(t), nil).leases(t.Context(), model.Project{Name: "p", Claims: "zk"})

	require.EqualError(t, err, `project p: no claim store named "zk"`)
}

func TestAdapters_CheckRefusesABdThatIsTooOld(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Version = "0.56.1"
	project := model.Project{Name: "p", Repo: "o/n", Tracker: model.TrackerGithub, Forge: model.ForgeGithub, Claims: model.ClaimsBeads, BeadsPrefix: "p"}

	require.EqualError(t, newAdapters(bd, nil).check(t.Context(), project), "obee needs bd 1.3.2 or newer for claims; found 0.56.1")

	bd.Version = "1.3.2"
	require.NoError(t, newAdapters(bd, nil).check(t.Context(), project))
}

func TestAdapters_TheHostTableHasAConstructorForEveryProviderAndNothingElse(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)

	for _, name := range model.HostProviders() {
		assert.Contains(t, table.hosts, name)
	}

	assert.Len(t, table.hosts, len(model.HostProviders()))
}

func hostProject(provider model.HostProvider, buildHost string) model.Project {
	return model.Project{
		Name: "p", Repo: "o/n", Tracker: model.TrackerGithub, Forge: model.ForgeGithub, Claims: model.ClaimsBeads, BeadsPrefix: "p", BuildHost: buildHost,
		Host: &model.HostConfig{Provider: provider, IdleMinutes: 60, MaxRun: time.Hour, DiskFloorGB: 50},
	}
}

func TestAdapters_HostBuildsTheProviderTheProjectNames(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)

	built, err := table.host(hostProject(model.HostSkyPilot, "lane-host"))
	require.NoError(t, err)
	assert.IsType(t, &skypilot.Host{}, built)

	_, err = table.host(hostProject("ssh", "lane-host"))
	require.EqualError(t, err, `project p: no host adapter named "ssh"`)

	_, err = table.host(model.Project{Name: "p"})
	require.EqualError(t, err, "project p: no host block, so no build host")

	built, err = table.host(hostProject(model.HostSkyPilot, "-oProxyCommand=x"))
	require.ErrorIs(t, err, skypilot.ErrConfig)
	assert.Nil(t, built)
}

func TestAdapters_CheckRefusesAHostProviderItDoesNotKnowAndAHostItCannotBuild(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)

	require.NoError(t, table.check(t.Context(), hostProject(model.HostSkyPilot, "lane-host")))
	require.ErrorContains(t, table.check(t.Context(), hostProject("ssh", "lane-host")), `no host adapter named "ssh"`)
	require.ErrorIs(t, table.check(t.Context(), hostProject(model.HostSkyPilot, "")), skypilot.ErrConfig)
}
