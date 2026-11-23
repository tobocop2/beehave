package main

import (
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	"github.com/tobocop2/beehave/internal/host/jobwait"
	"github.com/tobocop2/beehave/internal/host/link"
	"github.com/tobocop2/beehave/internal/host/skypilot"
	"github.com/tobocop2/beehave/internal/host/slurm"
	"github.com/tobocop2/beehave/internal/host/ssh"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc/proctest"
	"github.com/tobocop2/beehave/internal/store/beads"
	"github.com/tobocop2/beehave/internal/store/beads/beadstest"
	storegithub "github.com/tobocop2/beehave/internal/store/github"
	"github.com/tobocop2/beehave/internal/store/local"
)

// heldSlot is a slot that holds and releases nothing.
type heldSlot struct{}

func (heldSlot) Hold(context.Context, jobwait.Job) error    { return nil }
func (heldSlot) Release(context.Context, jobwait.Job) error { return nil }

func TestSlurmQueue_TheJobOfTheSkyPilotHostEndsAtTheRunLimitOfTheHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		limit time.Duration
		want  string
	}{
		{"whole minutes", 12 * time.Hour, "--time=720"},
		{"a part of a minute rounds up", 90 * time.Second, "--time=2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ssh := &proctest.Scripted{}
			ssh.Plan("42\n", "sbatch")

			queue := slurmQueue(link.New("lane-host", ssh), model.HostConfig{Provider: skypilot.Name, MaxRun: tc.limit}, heldSlot{})

			_, err := queue.Submit(t.Context(), model.JobSpec{Name: "gate", Command: "make check"})

			require.NoError(t, err)
			require.Len(t, ssh.Calls, 1)
			assert.Contains(t, strings.Join(ssh.Calls[0], " "), tc.want+" ", "the word sits before the wrapped command")
		})
	}
}

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

func TestAdapters_AgentBuildsTheAdapterOfEachAgentAndNothingForAnUnknownName(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)

	assert.Equal(t, claude.New(nil), table.agent(claude.Name))
	assert.Equal(t, opencode.New(), table.agent(opencode.Name))
	assert.Nil(t, table.agent(model.Agent("gpt")))
	assert.Len(t, table.agents, 2)
}

func TestAdapters_BuildTheAdapterTheProjectNames(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)
	project := model.Project{Name: "p", Repo: "o/n", Tracker: beads.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, Blocks: beadstest.Blocks("p")}

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
	good := model.Project{Name: "p", Repo: "o/n", Tracker: storegithub.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, Blocks: beadstest.Blocks("p")}

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
	good := model.Project{Name: "p", Repo: "o/n", Tracker: storegithub.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, Blocks: beadstest.Blocks("p")}
	require.NoError(t, table.check(t.Context(), good))

	badTracker := model.Project{Name: "p", Repo: "o/n", Tracker: beads.Tracker, Forge: forgegithub.Name}
	require.ErrorContains(t, table.check(t.Context(), badTracker), "the beads id prefix is required")

	badForge := model.Project{Name: "p", Repo: "bad", Tracker: beads.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, Blocks: beadstest.Blocks("p")}
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

	project.Blocks = beadstest.Blocks("p")
	project.Tools = []string{model.ToolGit, beads.Tool}
	bd := beadstest.NewBd(t)
	bd.Version = "0.56.1"
	require.ErrorContains(t, newAdapters(bd, nil, nil).check(t.Context(), project), "found 0.56.1")
}

func TestAdapters_AProjectWithNoForgeHasNoForgeToBuildAndNoForgeFields(t *testing.T) {
	t.Parallel()

	table := newAdapters(beadstest.NewBd(t), nil, nil)
	noForge := model.Project{Name: "p", Source: "/srv/repo", Tracker: beads.Tracker, Claims: beads.Claims, Blocks: beadstest.Blocks("p")}

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
	project := model.Project{Name: "p", Repo: "o/n", Tracker: storegithub.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, Blocks: beadstest.Blocks("p"), Tools: []string{model.ToolGit, beads.Tool, "gh"}}

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

	assert.Equal(t, beads.Block(), *registered.Trackers[beads.Tracker].Block)
	assert.Equal(t, beads.Block(), *registered.Claims[beads.Claims].Block)
	assert.Empty(t, registered.Trackers[beads.Tracker].Keys)
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
		Name: "p", Repo: "o/n", Tracker: storegithub.Tracker, Forge: forgegithub.Name, Claims: beads.Claims, Blocks: beadstest.Blocks("p"), BuildHost: buildHost,
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

// runningHost is a host.Host whose jobs run until the test ends them or the host cancels them. A cancel frees the disk.
type runningHost struct {
	host.Host

	mu       sync.Mutex
	free     uint64
	jobs     map[model.JobID]chan model.JobState
	canceled []model.JobID
	entered  chan model.JobID // each Wait says its job is entered; the guard has put the job in its fleet first
}

func (r *runningHost) Status(context.Context) (model.HostStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	return model.HostStatus{State: model.HostUp, Disk: model.DiskReading{FreeBytes: r.free, Readable: true}}, nil
}

func (r *runningHost) Submit(context.Context, model.JobSpec) (model.JobID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := model.JobID(strconv.Itoa(len(r.jobs) + 1))
	r.jobs[id] = make(chan model.JobState, 1)

	return id, nil
}

func (r *runningHost) Wait(_ context.Context, id model.JobID) (model.JobState, error) {
	r.mu.Lock()
	ended := r.jobs[id]
	r.mu.Unlock()

	r.entered <- id

	return <-ended, nil
}

// Cancel frees the disk after a short while, as a job that ends takes a moment to leave its files.
func (r *runningHost) Cancel(_ context.Context, id model.JobID) error {
	r.mu.Lock()
	r.canceled = append(r.canceled, id)
	r.mu.Unlock()

	time.Sleep(30 * time.Millisecond)

	r.mu.Lock()
	defer r.mu.Unlock()

	r.free = 60 << 30
	r.jobs[id] <- model.JobCanceled

	return nil
}

func TestAdapters_HostsBuiltForOneClusterShareOneFleetSoOnlyTheYoungestJobStops(t *testing.T) {
	t.Parallel()

	const gigabyte = 1 << 30

	inner := &runningHost{free: 60 * gigabyte, jobs: map[model.JobID]chan model.JobState{}, entered: make(chan model.JobID, 2)}
	table := newAdapters(beadstest.NewBd(t), nil, nil)
	table.diskPoll = time.Millisecond
	table.hosts[skypilot.Name] = hostAdapter{build: func(model.Project) (host.Host, error) { return inner, nil }}

	project := hostProject(skypilot.Name, "lane-host")
	older, err := table.host(project)
	require.NoError(t, err)
	younger, err := table.host(project)
	require.NoError(t, err)

	first, err := older.Submit(t.Context(), model.JobSpec{Name: "a"})
	require.NoError(t, err)
	second, err := younger.Submit(t.Context(), model.JobSpec{Name: "b"})
	require.NoError(t, err)

	olderEnd := make(chan model.JobState, 1)
	youngerEnd := make(chan model.JobState, 1)

	go func() {
		state, waitErr := older.Wait(t.Context(), first)
		assert.NoError(t, waitErr)

		olderEnd <- state
	}()

	require.Equal(t, first, <-inner.entered)

	go func() {
		state, waitErr := younger.Wait(t.Context(), second)
		assert.NoError(t, waitErr)

		youngerEnd <- state
	}()

	require.Equal(t, second, <-inner.entered)

	inner.mu.Lock()
	inner.free = 10 * gigabyte
	inner.mu.Unlock()

	state := <-youngerEnd
	assert.Equal(t, model.JobDiskFull, state, "the guard of the second host stops the job whose wait began last on the cluster")

	select {
	case inner.jobs[first] <- model.JobSucceeded:
	default:
	}

	assert.Equal(t, model.JobSucceeded, <-olderEnd)

	inner.mu.Lock()
	defer inner.mu.Unlock()

	assert.Equal(t, []model.JobID{second}, inner.canceled, "the older job is not stopped")
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

// The names are the values that the user writes in the config files, so a name keeps its value.
func TestAdapters_EveryNameKeepsTheValueThatTheConfigWrites(t *testing.T) {
	t.Parallel()

	names := []struct {
		name string
		got  string
		want string
	}{
		{"local tracker", string(local.Tracker), "local"},
		{"local claims", string(local.Claims), "local"},
		{"beads tracker", string(beads.Tracker), "beads"},
		{"beads claims", string(beads.Claims), "beads"},
		{"github tracker", string(storegithub.Tracker), "github"},
		{"github forge", string(forgegithub.Name), "github"},
		{"skypilot host", string(skypilot.Name), "skypilot"},
		{"ssh host", string(ssh.Name), "ssh"},
		{"slurm host", string(slurm.Name), "slurm"},
		{"claude agent", string(claude.Name), "claude"},
		{"opencode agent", string(opencode.Name), "opencode"},
	}

	for _, tc := range names {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, tc.got)
		})
	}
}
