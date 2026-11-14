package main

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent/claude"
	"github.com/tobocop2/beehave/internal/agent/opencode"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/forge"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/slurm"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
	"github.com/tobocop2/beehave/internal/store/local"
)

func TestAdapters_TheTableHasAConstructorForEveryNameAndNothingElse(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	trackers := []model.TrackerName{local.Tracker, beads.Tracker, storegithub.Tracker}
	for _, name := range trackers {
		assert.Contains(t, table.trackers, name)
		assert.NotNil(t, table.trackers[name].build, "tracker %s has a constructor", name)
	}

	assert.Contains(t, table.forges, forgegithub.Name)
	assert.NotNil(t, table.forges[forgegithub.Name].build)

	assert.Len(t, table.trackers, len(trackers))
	assert.Len(t, table.forges, 1)
}

func TestAdapters_AgentBuildsTheClaudeAdapterAndNothingForAnAgentWithoutOne(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	assert.Equal(t, claude.New(nil), table.agent(claude.Name))
	assert.Nil(t, table.agent(opencode.Name))
}

func TestAdapters_BuildTheAdapterTheProjectNames(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)
	project := model.Project{Name: "p", Repo: "o/n", Tracker: beads.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, BeadsPrefix: "p"}

	tracker, err := table.tracker(t.Context(), project)
	require.NoError(t, err)
	assert.NotNil(t, tracker)

	project.Tracker = storegithub.Tracker
	tracker, err = table.tracker(t.Context(), project)
	require.NoError(t, err)
	assert.NotNil(t, tracker)

	forge, err := table.forge(project)
	require.NoError(t, err)
	assert.NotNil(t, forge)
}

func TestAdapters_RefuseWhatTheyCannotBuild(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	_, err := table.tracker(t.Context(), model.Project{Name: "p", Tracker: "jira"})
	require.EqualError(t, err, `project p: no tracker adapter named "jira"`)

	_, err = table.forge(model.Project{Name: "p", Forge: "gitlab"})
	require.EqualError(t, err, `project p: no forge adapter named "gitlab"`)

	tracker, err := table.tracker(t.Context(), model.Project{Name: "p", Tracker: beads.Tracker})
	require.ErrorContains(t, err, "the beads id prefix is required")
	assert.Nil(t, tracker)

	tracker, err = table.tracker(t.Context(), model.Project{Name: "p", Tracker: storegithub.Tracker, Repo: "bad"})
	require.Error(t, err)
	assert.Nil(t, tracker)

	forge, err := table.forge(model.Project{Name: "p", Forge: forgegithub.Name, Repo: "bad"})
	require.Error(t, err)
	assert.Nil(t, forge)
}

func TestAdapters_CheckTrackerAndForgeAcceptsAProjectWhoseAdaptersBuild(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)
	good := model.Project{Name: "p", Repo: "o/n", Tracker: storegithub.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, BeadsPrefix: "p"}

	require.NoError(t, table.checkTrackerAndForge(t.Context(), good))
}

func TestAdapters_CheckTrackerAndForgeReportsTheFailureOfTheCheckOfATracker(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)
	table.trackers["ticket"] = trackerAdapter{check: func(context.Context, model.Project) error { return errors.New("no ticket server") }}

	err := table.checkTrackerAndForge(t.Context(), model.Project{Name: "p", Tracker: "ticket"})

	require.EqualError(t, err, "no ticket server")
}

func TestAdapters_CheckTrackerAndForgeRefusesAProjectWhoseAdapterCannotBeBuilt(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	err := table.checkTrackerAndForge(t.Context(), model.Project{Name: "p", Tracker: "jira"})
	require.EqualError(t, err, `project p: no tracker adapter named "jira"`)

	err = table.checkTrackerAndForge(t.Context(), model.Project{Name: "p", Repo: "o/n", Tracker: storegithub.Tracker, Forge: "gitlab"})
	require.EqualError(t, err, `project p: no forge adapter named "gitlab"`)
}

func TestAdapters_CheckBuildsTheTrackerTheForgeAndTheLeaseStoreOfAProject(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)
	good := model.Project{Name: "p", Repo: "o/n", Tracker: storegithub.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, BeadsPrefix: "p"}
	require.NoError(t, table.check(t.Context(), good))

	badTracker := model.Project{Name: "p", Repo: "o/n", Tracker: beads.Tracker, Forge: forgegithub.Name}
	require.ErrorContains(t, table.check(t.Context(), badTracker), "the beads id prefix is required")

	badForge := model.Project{Name: "p", Repo: "bad", Tracker: beads.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, BeadsPrefix: "p"}
	require.Error(t, table.check(t.Context(), badForge))

	noPrefix := model.Project{Name: "p", Repo: "o/n", Tracker: storegithub.Tracker, Forge: forgegithub.Name, Claims: beads.Claims}
	require.ErrorContains(t, table.check(t.Context(), noPrefix), "the beads id prefix is required")
}

func stateAt(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

func TestAdapters_AProjectWithNoHostBlockBuildsNoHostSoItNeedsNeitherSkyNorSlurm(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, nil, nil)
	table.hosts = map[model.HostProvider]hostAdapter{
		skypilot.Name: {build: func(model.Project) (host.Host, error) {
			t.Error("a local project built a host adapter")

			return nil, nil //nolint:nilnil // the test fails above
		}},
	}

	_, err := table.host(localProjectNamed("p"))

	require.ErrorContains(t, err, "no host block")
}

func localProjectNamed(name string) model.Project {
	return model.Project{Name: name, Source: "/srv/repo", Tracker: local.Tracker, Claims: local.Claims}
}

func TestAdapters_TheLocalTrackerOpensLocalDbInTheStateFolderAndRefusesAnUnknownItem(t *testing.T) {
	t.Parallel()

	state := t.TempDir()
	table := newAdapters(nil, stateAt(state), nil)

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

	table := newAdapters(nil, stateAt(t.TempDir()), nil)

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

	opened, err := newAdapters(nil, stateAt(t.TempDir()), nil).itemOpener()[local.Tracker](t.Context(), localProjectNamed("p"))
	require.NoError(t, err)
	require.NoError(t, opened.Add(t.Context(), "fix-login", "Fix login", "body"))
	require.NoError(t, opened.Close())

	opened, err = newAdapters(nil, func() (string, error) { return "", errFake }, nil).itemOpener()[local.Tracker](t.Context(), localProjectNamed("p"))
	require.ErrorIs(t, err, errFake)
	assert.Nil(t, opened)
}

func TestAdapters_ALocalStoreThatCannotOpenIsAnError(t *testing.T) {
	t.Parallel()

	failing := func() (string, error) { return "", errFake }
	table := newAdapters(nil, failing, nil)

	tracker, err := table.tracker(t.Context(), localProjectNamed("p"))
	require.ErrorIs(t, err, errFake)
	assert.Nil(t, tracker)

	_, err = table.leases(t.Context(), localProjectNamed("p"))
	require.ErrorIs(t, err, errFake)

	file := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	_, err = newAdapters(nil, stateAt(filepath.Join(file, "state")), nil).tracker(t.Context(), localProjectNamed("p"))
	require.Error(t, err)
}

func TestAdapters_ALocalProjectNeedsNoBdAndCheckBuildsNothingOnDisk(t *testing.T) {
	t.Parallel()

	state := filepath.Join(t.TempDir(), "state")
	table := newAdapters(nil, stateAt(state), nil)
	project := localProjectNamed("p")

	require.NoError(t, table.check(t.Context(), project))

	_, err := os.Stat(state)
	require.ErrorIs(t, err, os.ErrNotExist, "check leaves the state folder alone")
}

func TestAdapters_CheckRefusesABeadsClaimStoreWithNoPrefixEvenWhenTheTrackerIsLocal(t *testing.T) {
	t.Parallel()

	project := model.Project{Name: "p", Source: "/srv/repo", Tracker: local.Tracker, Claims: beads.Claims}

	require.ErrorContains(t, newAdapters(beadstest.NewBd(t), nil, nil).check(t.Context(), project), "the beads id prefix is required")

	project.BeadsPrefix = "p"
	project.Tools = []string{model.ToolGit, beads.Tool}
	bd := beadstest.NewBd(t)
	bd.Version = "0.56.1"
	require.ErrorContains(t, newAdapters(bd, nil, nil).check(t.Context(), project), "found 0.56.1")
}

func TestAdapters_AProjectWithNoForgeHasNoForgeToBuildAndNoForgeFields(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)
	noForge := model.Project{Name: "p", Source: "/srv/repo", Tracker: beads.Tracker, Claims: beads.Claims, BeadsPrefix: "p"}

	built, err := table.forge(noForge)
	require.EqualError(t, err, `project p: no forge; set forge = "github" in the project file`)
	require.ErrorIs(t, err, forge.ErrNoForge)
	assert.Nil(t, built)

	require.NoError(t, table.check(t.Context(), noForge))
}

func TestAdapters_ABeadsClaimStoreWithNoPrefixAndAnUnknownClaimsNameAreRefusedByLeasesAndCheck(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	rows, err := table.leases(t.Context(), model.Project{Name: "p", Claims: beads.Claims})
	require.ErrorContains(t, err, "the beads id prefix is required")
	assert.Nil(t, rows.Rows)

	require.EqualError(t, table.check(t.Context(), model.Project{Name: "p", Source: "/srv/repo", Tracker: local.Tracker, Claims: "zk"}), `project p: no claim store named "zk"`)
}

func TestStateFolder_AnswersWithTheFolderOfTheOptions(t *testing.T) {
	t.Parallel()

	dir, err := stateFolder("/var/beehave")()

	require.NoError(t, err)
	assert.Equal(t, "/var/beehave", dir)
}

func TestAdapters_AClaimsNameThatIsNeitherLocalNorBeadsIsRefused(t *testing.T) {
	t.Parallel()

	_, err := newAdapters(beadstest.NewBd(t), nil, nil).leases(t.Context(), model.Project{Name: "p", Claims: "zk"})

	require.EqualError(t, err, `project p: no claim store named "zk"`)
}

func TestAdapters_CheckRefusesABdThatIsTooOld(t *testing.T) {
	t.Parallel()

	bd := beadstest.NewBd(t)
	bd.Version = "0.56.1"
	project := model.Project{Name: "p", Repo: "o/n", Tracker: storegithub.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, BeadsPrefix: "p", Tools: []string{model.ToolGit, beads.Tool, "gh"}}

	require.EqualError(t, newAdapters(bd, nil, nil).check(t.Context(), project), "beehave needs bd 1.3.2 or newer for claims; found 0.56.1")

	project.Tools = []string{model.ToolGit, "gh"}
	require.NoError(t, newAdapters(bd, nil, nil).check(t.Context(), project), "a project whose tools omit bd runs no bd version check")

	bd.Version = "1.3.2"
	project.Tools = []string{model.ToolGit, beads.Tool, "gh"}
	require.NoError(t, newAdapters(bd, nil, nil).check(t.Context(), project))
}

func TestAdapters_RegistryHoldsTheNamesTheKeysAndTheToolsOfTheEntries(t *testing.T) {
	t.Parallel()

	registered := newAdapters(beadstest.NewBd(t), nil, nil).registry()

	assert.Equal(t, []string{"beads_prefix"}, registered.Trackers[beads.Tracker].Keys)
	assert.Equal(t, []string{beads.Tool}, registered.Claims[beads.Claims].Tools)
	assert.Equal(t, []string{"repo"}, registered.Forges[forgegithub.Name].Keys)
	assert.True(t, registered.Hosts[skypilot.Name].Idle)
	assert.False(t, registered.Hosts[ssh.Name].Idle)
	assert.Empty(t, registered.Trackers[local.Tracker].Keys)
	assert.Contains(t, registered.Agents, opencode.Name, "an agent with no constructor is still a name of the config")
}

func TestClaims_NoForgeNameIsAClaimStore(t *testing.T) {
	t.Parallel()

	registered := newAdapters(beadstest.NewBd(t), nil, nil).registry()

	assert.Contains(t, registered.Forges, forgegithub.Name)

	for forgeName := range registered.Forges {
		assert.NotContains(t, registered.Claims, model.ClaimsName(forgeName), "the forge %s and the claim store are separate registries", forgeName)
	}

	assert.Equal(t, []model.ClaimsName{beads.Claims, local.Claims}, slices.Sorted(maps.Keys(registered.Claims)))
}

func TestAdapters_ANewAdapterNeedsOnlyAnEntryOfTheTable(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)
	table.trackers["ticket"] = trackerAdapter{registration: registration{Entry: config.Entry{Keys: []string{"repo"}, Tools: []string{"tk"}}}}

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "projects"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "projects", "p.star"), []byte("project(repo = \"o/n\", tracker = \"ticket\", base_branch = \"main\", gate = \"make check\", test_report = \"r.xml\")\n"), 0o600))

	cfg, problems := config.LoadFolder(dir, map[string]string{}, table.registry())

	require.Empty(t, problems)
	assert.Equal(t, model.TrackerName("ticket"), cfg.Projects["p"].Tracker)
	assert.Equal(t, []string{model.ToolGit, "tk"}, cfg.Projects["p"].Tools, "the tools of the entry reach the project with no edit of the config package")
}

func TestAdapters_TheClaimsMapHoldsAnOpenerAndACheckForEachClaimStore(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	require.Len(t, table.claims, 2)

	for name, entry := range table.claims {
		assert.NotNil(t, entry.open, "claim store %s has an opener", name)
		assert.NotNil(t, entry.check, "claim store %s has a check", name)
	}
}

func TestAdapters_TheHostTableHasAConstructorForEveryProviderAndNothingElse(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	for _, name := range hostProviders {
		assert.Contains(t, table.hosts, name)
	}

	assert.Len(t, table.hosts, len(hostProviders))
}

var hostProviders = []model.HostProvider{skypilot.Name, ssh.Name, slurm.Name}

func TestAdapters_EveryProviderBuildsAHostThatBanksItsWorkOnTeardown(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	for _, name := range hostProviders {
		built, err := table.host(hostProject(name, "lane-host"))
		require.NoError(t, err, name)

		_, banks := built.(bankingHost)
		assert.True(t, banks, "%s: a project that selects the provider can run host_teardown", name)
	}
}

func hostProject(provider model.HostProvider, buildHost string) model.Project {
	return model.Project{
		Name: "p", Repo: "o/n", Tracker: storegithub.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, BeadsPrefix: "p", BuildHost: buildHost,
		Host: &model.HostConfig{Provider: provider, IdleMinutes: 60, MaxRun: time.Hour, DiskFloorGB: 50},
	}
}

func TestAdapters_HostBuildsTheProviderTheProjectNames(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	built, err := table.host(hostProject(skypilot.Name, "lane-host"))
	require.NoError(t, err)
	assert.NotNil(t, built)

	built, err = table.host(hostProject(ssh.Name, "lane-host"))
	require.NoError(t, err)
	assert.NotNil(t, built)

	built, err = table.host(hostProject(slurm.Name, "lane-host"))
	require.NoError(t, err)
	assert.NotNil(t, built)

	built, err = table.host(hostProject(slurm.Name, "-oProxyCommand=x"))
	require.ErrorIs(t, err, link.ErrConfig)
	assert.Nil(t, built)

	built, err = table.host(hostProject(ssh.Name, "-oProxyCommand=x"))
	require.ErrorIs(t, err, link.ErrConfig)
	assert.Nil(t, built)

	_, err = table.host(model.Project{Name: "p"})
	require.EqualError(t, err, "project p: no host block, so no build host")

	built, err = table.host(hostProject(skypilot.Name, "-oProxyCommand=x"))
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
	table := newAdapters(beadstest.NewBd(t), nil, nil)
	table.diskPoll = time.Millisecond
	table.hosts[skypilot.Name] = hostAdapter{build: func(model.Project) (host.Host, error) { return freeing, nil }}

	project := hostProject(skypilot.Name, "lane-host")
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
	table := newAdapters(beadstest.NewBd(t), nil, nil)
	table.hosts[skypilot.Name] = hostAdapter{build: func(model.Project) (host.Host, error) { return low, nil }}

	built, err := table.host(hostProject(skypilot.Name, "lane-host"))
	require.NoError(t, err)

	_, err = built.Submit(t.Context(), model.JobSpec{Name: "gate-p"})
	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Zero(t, low.submits)

	enough := &statusHost{disk: model.DiskReading{FreeBytes: 60 * gigabyte, Readable: true}}
	table.hosts[skypilot.Name] = hostAdapter{build: func(model.Project) (host.Host, error) { return enough, nil }}

	built, err = table.host(hostProject(skypilot.Name, "lane-host"))
	require.NoError(t, err)

	_, err = built.Submit(t.Context(), model.JobSpec{Name: "gate-p"})
	require.NoError(t, err)
	assert.Equal(t, 1, enough.submits)
}

func TestAdapters_CheckRefusesAHostProviderItDoesNotKnowAndAHostItCannotBuild(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	require.NoError(t, table.check(t.Context(), hostProject(skypilot.Name, "lane-host")))
	require.NoError(t, table.check(t.Context(), hostProject(ssh.Name, "lane-host")), "building the ssh host runs no ssh")
	require.NoError(t, table.check(t.Context(), hostProject(slurm.Name, "lane-host")), "building the slurm host runs no ssh")
	require.ErrorIs(t, table.check(t.Context(), hostProject(slurm.Name, "-bad")), link.ErrConfig)
	require.ErrorContains(t, table.check(t.Context(), hostProject("", "lane-host")), "no host adapter named")
	require.ErrorIs(t, table.check(t.Context(), hostProject(skypilot.Name, "")), skypilot.ErrConfig)
}

func TestHostErrors_ListsEachSentinelOfTheHostEntriesOnce(t *testing.T) {
	t.Parallel()

	table := newAdapters(nil, nil, nil)

	assert.Equal(t, []error{skypilot.ErrConfig, link.ErrConfig, ssh.ErrLinger, ssh.ErrSystemd, ssh.ErrDraining}, table.hostErrors())
}

func TestHostErrors_FollowsTheEntriesOfTheTable(t *testing.T) {
	t.Parallel()

	errOne, errTwo := errors.New("one"), errors.New("two")
	table := newAdapters(nil, nil, nil)
	table.hosts = map[model.HostProvider]hostAdapter{
		ssh.Name:      {sentinels: []error{errTwo, errOne}},
		skypilot.Name: {sentinels: []error{errOne}},
	}

	assert.Equal(t, []error{errOne, errTwo}, table.hostErrors())

	table.hosts = nil

	assert.Empty(t, table.hostErrors())
}
