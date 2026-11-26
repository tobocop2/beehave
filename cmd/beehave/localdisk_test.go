package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/agent"
	"github.com/tobocop2/beehave/internal/config"
	"github.com/tobocop2/beehave/internal/flow"
	forgegithub "github.com/tobocop2/beehave/internal/forge/github"
	"github.com/tobocop2/beehave/internal/host"
	"github.com/tobocop2/beehave/internal/model"
	"github.com/tobocop2/beehave/internal/proc"
)

const diskGB = 1 << 30

// floorProjects has a project with a repository, "p", and a plain project, "plain", each with a host block that holds a floor of 50 GB and no wait,
// and a project with no host block, "bare".
func floorProjects() map[string]config.Project {
	block := &model.HostConfig{DiskFloorGB: 50}

	return map[string]config.Project{
		"p":     {Project: model.Project{Name: "p", Repo: "owner/name", Forge: forgegithub.Name, BaseBranch: "main", Host: block}},
		"plain": {Project: model.Project{Name: "plain", Host: block}},
		"bare":  {Project: model.Project{Name: "bare", Repo: "owner/bare", Forge: forgegithub.Name, BaseBranch: "main"}},
	}
}

// diskWith returns a localDisk over two real folders whose free space is rootGB and stateGB, as the fake free function says. The folders are in the result.
func diskWith(t *testing.T, rootGB, stateGB uint64) localDisk {
	t.Helper()

	root, state := t.TempDir(), t.TempDir()

	return localDisk{
		projects: floorProjects(), root: root, state: state, poll: time.Millisecond,
		free: func(_ context.Context, folder string) (uint64, error) {
			if folder == root {
				return rootGB * diskGB, nil
			}

			return stateGB * diskGB, nil
		},
	}
}

// lowDisk has both folders under the floor, and roomyDisk has both above it.
func lowDisk(t *testing.T) localDisk {
	t.Helper()

	return diskWith(t, 1, 1)
}

func roomyDisk(t *testing.T) localDisk {
	t.Helper()

	return diskWith(t, 60, 60)
}

// gitFixture is a worktrees value over the fake git of ensure_test.go, for the projects of floorProjects.
func gitFixture(t *testing.T, disk localDisk) (worktrees, *fakeGit) {
	t.Helper()

	trees, git, _ := ensureFixture(t)
	trees.projects, trees.disk = floorProjects(), disk

	return trees, git
}

func TestLaneStart_ANewWorktreeIsNotAddedUnderTheFloor(t *testing.T) {
	t.Parallel()

	disk := lowDisk(t)
	trees, git := gitFixture(t, disk)

	_, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")

	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Empty(t, git.calls, "git neither cloned nor fetched nor added a worktree")
	assert.NoDirExists(t, filepath.Join(trees.root, "p", "bb-1"))
}

func TestLaneStart_ANewPlainFolderIsNotMadeUnderTheFloor(t *testing.T) {
	t.Parallel()

	trees := worktrees{root: t.TempDir(), projects: floorProjects(), disk: lowDisk(t)}

	_, err := trees.Ensure(t.Context(), "plain", "1", "", "")

	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.NoDirExists(t, filepath.Join(trees.root, "plain", "1"))
}

func TestLaneStart_AnAgentStepDoesNotStartUnderTheFloor(t *testing.T) {
	t.Parallel()

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.disk = lowDisk(t)
	started := countRuns(&f.agents)

	got, err := f.agents.Run(t.Context(), request())

	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeNotStarted, got.Outcome)
	assert.Contains(t, got.Note, "free-space floor")
	assert.Zero(t, *started, "no agent process started")
}

// countRuns wraps the run of the agents so that the number of agent processes it starts can be read.
func countRuns(a *agents) *int {
	started := 0
	inner := a.run
	a.run = func(ctx context.Context, spec agent.Spec) agent.Result {
		started++

		return inner(ctx, spec)
	}

	return &started
}

func TestLaneStart_ALocalCommandStepDoesNotStartUnderTheFloor(t *testing.T) {
	t.Parallel()

	started := 0
	runs := programs{
		trees: treeAt(t.TempDir()), disk: lowDisk(t),
		exec: func(spec proc.ExecRunner) proc.Runner {
			started++

			return spec
		},
	}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"true"}})

	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Zero(t, started, "no program started")
}

func TestLaneStart_ASandboxedGateDoesNotStartUnderTheFloor(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	runs := programs{trees: treeAt(t.TempDir()), sandbox: offProject(t, fake), disk: lowDisk(t)}

	_, err := runs.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"true"}, Sandbox: &model.Sandbox{}})

	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Empty(t, fake.argv, "the enforcer ran no command")
}

func TestLaneStart_ALowStateFolderRefusesEvenWhenTheWorktreeRootHasSpace(t *testing.T) {
	t.Parallel()

	disk := diskWith(t, 60, 1)
	trees := worktrees{root: disk.root, projects: floorProjects(), disk: disk}

	_, err := trees.Ensure(t.Context(), "plain", "1", "", "")

	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Contains(t, err.Error(), disk.state)
	assert.NotContains(t, err.Error(), disk.root)
}

func TestLaneStart_ALowWorktreeRootRefusesEvenWhenTheStateFolderHasSpace(t *testing.T) {
	t.Parallel()

	disk := diskWith(t, 1, 60)
	trees := worktrees{root: disk.root, projects: floorProjects(), disk: disk}

	_, err := trees.Ensure(t.Context(), "plain", "1", "", "")

	require.ErrorIs(t, err, host.ErrDiskLow)
	assert.Contains(t, err.Error(), disk.root)
	assert.NotContains(t, err.Error(), disk.state)
}

func TestLaneStart_AboveTheFloorEveryStartPathRuns(t *testing.T) {
	t.Parallel()

	disk := roomyDisk(t)

	trees, git := gitFixture(t, disk)
	_, err := trees.Ensure(t.Context(), "p", "bb-1", "", "")
	require.NoError(t, err)
	assert.NotEmpty(t, git.calls, "a new worktree was added")

	plain := worktrees{root: t.TempDir(), projects: floorProjects(), disk: disk}
	dir, err := plain.Ensure(t.Context(), "plain", "1", "", "")
	require.NoError(t, err)
	assert.DirExists(t, dir)

	f := newAgentFixture("session=s1\n", agent.Result{Outcome: agent.OutcomeExited})
	f.agents.disk = disk
	started := countRuns(&f.agents)
	got, err := f.agents.Run(t.Context(), request())
	require.NoError(t, err)
	assert.Equal(t, agent.OutcomeExited, got.Outcome)
	assert.Equal(t, 1, *started)

	ran := 0
	local := programs{trees: treeAt(t.TempDir()), disk: disk, exec: func(spec proc.ExecRunner) proc.Runner {
		ran++

		return spec
	}}
	_, err = local.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"true"}})
	require.NoError(t, err)
	assert.Equal(t, 1, ran)

	fake := &fakeEnforcer{}
	boxed := programs{trees: treeAt(t.TempDir()), sandbox: offProject(t, fake), disk: disk}
	_, err = boxed.Run(t.Context(), flow.CommandRequest{Project: "p", Item: "1", Run: []string{"true"}, Sandbox: &model.Sandbox{}})
	require.NoError(t, err)
	assert.Equal(t, []string{"true"}, fake.argv)
}

func TestLaneStart_AResumeThatFindsItsPlainFolderReadsNoDisk(t *testing.T) {
	t.Parallel()

	trees := worktrees{root: t.TempDir(), projects: floorProjects(), disk: lowDisk(t)}
	require.NoError(t, os.MkdirAll(filepath.Join(trees.root, "plain", "1"), folderMode))

	dir, err := trees.Ensure(t.Context(), "plain", "1", "", "")

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(trees.root, "plain", "1"), dir)
}

func TestLaneStart_TheRefusalNamesTheFolderAndTheSetting(t *testing.T) {
	t.Parallel()

	disk := lowDisk(t)
	trees := worktrees{root: disk.root, projects: floorProjects(), disk: disk}

	_, err := trees.Ensure(t.Context(), "plain", "1", "", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), disk.root)
	assert.Contains(t, err.Error(), "1 GB free, floor 50 GB")
	assert.Contains(t, err.Error(), "host.disk_floor_gb")
}

func TestLaneStart_AProjectWithNoHostBlockIsNotChecked(t *testing.T) {
	t.Parallel()

	disk := lowDisk(t)
	disk.free = func(context.Context, string) (uint64, error) {
		t.Error("a project with no host block reads no disk")

		return 0, nil
	}

	for _, project := range []string{"bare", "unknown"} {
		require.NoError(t, disk.check(t.Context(), project), project)
	}

	disk.free = nil

	require.NoError(t, disk.check(t.Context(), "p"), "a disk with no free function checks nothing")
}

func TestLaneStart_ALocalStartWaitsForTheMinutesOfTheHostBlock(t *testing.T) {
	t.Parallel()

	reads := 0
	disk := lowDisk(t)
	disk.projects = map[string]config.Project{"p": {Project: model.Project{Name: "p", Host: &model.HostConfig{DiskFloorGB: 50, DiskWaitMinutes: 1}}}}
	disk.free = func(context.Context, string) (uint64, error) {
		reads++
		if reads < 3 {
			return 1 * diskGB, nil
		}

		return 60 * diskGB, nil
	}

	require.NoError(t, disk.check(t.Context(), "p"))
	assert.Greater(t, reads, 2, "the start read the disk again while it waited")
}

func TestNewLocalDisk_MeasuresTheTreesFolderAndTheStateFolderWithDf(t *testing.T) {
	t.Parallel()

	disk := newLocalDisk(floorProjects(), "/state")

	assert.Equal(t, "/state/trees", disk.root)
	assert.Equal(t, "/state", disk.state)
	assert.NotNil(t, disk.free)
	assert.Equal(t, defaultDiskPoll, disk.poll)
}

func TestLaneStart_AGateUnderTheSandboxDoesNotStartUnderTheFloor(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	box := sandboxFor(t, fake, nil)
	gate := projectGate{projects: box.projects, programs: programs{sandbox: box, disk: lowDisk(t)}}

	out, code := gate.shellFor("p")(t.Context(), t.TempDir(), "make check")

	assert.Equal(t, 1, code)
	assert.Contains(t, out, "free-space floor")
	assert.Empty(t, fake.argv, "the enforcer ran no command")

	out, code = gate.programShellFor("p")(t.Context(), t.TempDir(), t.TempDir(), "make check")

	assert.Equal(t, 1, code)
	assert.Contains(t, out, "free-space floor")
	assert.Empty(t, fake.argv, "the enforcer ran no job")
}

func TestLaneStart_AGateUnderTheSandboxRunsAboveTheFloor(t *testing.T) {
	t.Parallel()

	fake := &fakeEnforcer{}
	box := sandboxFor(t, fake, nil)
	gate := projectGate{projects: box.projects, programs: programs{sandbox: box, disk: roomyDisk(t)}}

	_, code := gate.shellFor("p")(t.Context(), t.TempDir(), "make check")

	assert.Zero(t, code)
	assert.Equal(t, []string{"sh", "-c", "make check"}, fake.argv)
}
