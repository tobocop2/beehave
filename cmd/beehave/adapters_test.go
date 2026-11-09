package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/agent/opencode"
	"github.com/tobocop2/beehave/internal/forge"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
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

func TestAdapters_AgentBuildsTheAdapterOfEachAgentAndNothingForAnUnknownName(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)

	assert.Equal(t, claude.New(), table.agent(model.AgentClaude))
	assert.Equal(t, opencode.New(), table.agent(model.AgentOpenCode))
	assert.Nil(t, table.agent(model.Agent("gpt")))
	assert.Len(t, table.agents, 2)
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

func TestAdapters_AProjectWithNoHostBlockBuildsNoHostSoItNeedsNeitherSkyNorSlurm(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, nil)
	table.hosts = map[model.HostProvider]func(model.Project) (host.Host, error){
		model.HostSkyPilot: func(model.Project) (host.Host, error) {
			t.Error("a local project built a host adapter")

			return nil, nil //nolint:nilnil // the test fails above
		},
	}

	_, err := table.host(localProjectNamed("p"))

	require.ErrorContains(t, err, "no host block")
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
	require.EqualError(t, err, "no item fix-login in project p. Add it: beehave item add")
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

	dir, err := stateFolder("/var/beehave")()

	require.NoError(t, err)
	assert.Equal(t, "/var/beehave", dir)
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

	require.EqualError(t, newAdapters(bd, nil).check(t.Context(), project), "beehave needs bd 1.3.2 or newer for claims; found 0.56.1")

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

func TestAdapters_EveryProviderBuildsAHostThatBanksItsWorkOnTeardown(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)

	for _, name := range model.HostProviders() {
		built, err := table.host(hostProject(name, "lane-host"))
		require.NoError(t, err, name)

		_, banks := built.(bankingHost)
		assert.True(t, banks, "%s: a project that selects the provider can run host_teardown", name)
	}
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
	assert.NotNil(t, built)

	built, err = table.host(hostProject(model.HostSSH, "lane-host"))
	require.NoError(t, err)
	assert.NotNil(t, built)

	built, err = table.host(hostProject(model.HostSlurm, "lane-host"))
	require.NoError(t, err)
	assert.NotNil(t, built)

	built, err = table.host(hostProject(model.HostSlurm, "-oProxyCommand=x"))
	require.ErrorIs(t, err, link.ErrConfig)
	assert.Nil(t, built)

	built, err = table.host(hostProject(model.HostSSH, "-oProxyCommand=x"))
	require.ErrorIs(t, err, link.ErrConfig)
	assert.Nil(t, built)

	_, err = table.host(model.Project{Name: "p"})
	require.EqualError(t, err, "project p: no host block, so no build host")

	built, err = table.host(hostProject(model.HostSkyPilot, "-oProxyCommand=x"))
	require.ErrorIs(t, err, skypilot.ErrConfig)
	assert.Nil(t, built)
}

// statusHost is a host.Host that reports one disk reading and records its Submit calls.
type statusHost struct {
	host.Host

	disk    model.DiskReading
	grow    uint64 // free bytes added after each Status call
	submits int
}

func (s *statusHost) Status(context.Context) (model.HostStatus, error) {
	status := model.HostStatus{Disk: s.disk}
	s.disk.FreeBytes += s.grow

	return status, nil
}

func TestAdapters_HostWaitsForDiskSpaceForTheMinutesTheProjectAllows(t *testing.T) {
	t.Parallel()

	const gigabyte = 1 << 30

	freeing := &statusHost{disk: model.DiskReading{FreeBytes: 10 * gigabyte, Readable: true}, grow: 30 * gigabyte}
	table := newAdapters(beadstest.NewBd(t), nil)
	table.diskPoll = time.Millisecond
	table.hosts[model.HostSkyPilot] = func(model.Project) (host.Host, error) { return freeing, nil }

	project := hostProject(model.HostSkyPilot, "lane-host")
	project.Host.DiskWaitMinutes = 1

	built, err := table.host(project)
	require.NoError(t, err)

	_, err = built.Submit(t.Context(), model.JobSpec{Name: "gate-p"})
	require.NoError(t, err)
	assert.Equal(t, 1, freeing.submits)
}

func (s *statusHost) Submit(context.Context, model.JobSpec) (model.JobID, error) {
	s.submits++

	return "job-1", nil
}

func TestAdapters_HostRefusesAJobWhenTheDiskIsUnderTheFloor(t *testing.T) {
	t.Parallel()

	const gigabyte = 1 << 30

	low := &statusHost{disk: model.DiskReading{FreeBytes: 10 * gigabyte, Readable: true}}
	table := newAdapters(beadstest.NewBd(t), nil)
	table.hosts[model.HostSkyPilot] = func(model.Project) (host.Host, error) { return low, nil }

	built, err := table.host(hostProject(model.HostSkyPilot, "lane-host"))
	require.NoError(t, err)

	_, err = built.Submit(t.Context(), model.JobSpec{Name: "gate-p"})
	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Zero(t, low.submits)

	enough := &statusHost{disk: model.DiskReading{FreeBytes: 60 * gigabyte, Readable: true}}
	table.hosts[model.HostSkyPilot] = func(model.Project) (host.Host, error) { return enough, nil }

	built, err = table.host(hostProject(model.HostSkyPilot, "lane-host"))
	require.NoError(t, err)

	_, err = built.Submit(t.Context(), model.JobSpec{Name: "gate-p"})
	require.NoError(t, err)
	assert.Equal(t, 1, enough.submits)
}

func TestAdapters_CheckRefusesAHostProviderItDoesNotKnowAndAHostItCannotBuild(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil)

	require.NoError(t, table.check(t.Context(), hostProject(model.HostSkyPilot, "lane-host")))
	require.NoError(t, table.check(t.Context(), hostProject(model.HostSSH, "lane-host")), "building the ssh host runs no ssh")
	require.NoError(t, table.check(t.Context(), hostProject(model.HostSlurm, "lane-host")), "building the slurm host runs no ssh")
	require.ErrorIs(t, table.check(t.Context(), hostProject(model.HostSlurm, "-bad")), link.ErrConfig)
	require.ErrorContains(t, table.check(t.Context(), hostProject("", "lane-host")), "no host adapter named")
	require.ErrorIs(t, table.check(t.Context(), hostProject(model.HostSkyPilot, "")), skypilot.ErrConfig)
}
